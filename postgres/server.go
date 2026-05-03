// Package postgres is the public API surface: Start a server, get a DSN,
// hand it to pgx.
package postgres

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/kaeawc/pgmem-go/catalog"
	"github.com/kaeawc/pgmem-go/postgres/wire"
	"github.com/kaeawc/pgmem-go/storage"
	"github.com/kaeawc/pgmem-go/types"
)

// Server is a running pgmem instance. Stop is idempotent.
type Server interface {
	DSN() string
	Stop()
	SetNow(t time.Time)
	// Seed populates a hardcoded catalog table with rows. It exists so
	// M1 acceptance tests can fill the bootstrap `users` table without
	// going through INSERT (which lands in M2).
	Seed(table string, rows ...[]any) error
}

// Option configures a Server at startup.
type Option func(*config)

type config struct{}

// Start boots a pgmem server bound to a free TCP port and registers
// teardown with the test. Target boot time is under 100ms.
func Start(t testing.TB, opts ...Option) (Server, error) {
	t.Helper()

	var cfg config
	for _, o := range opts {
		o(&cfg)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}

	sch := catalog.NewSchema()
	eng := storage.NewEngine()
	bootstrapUsers(sch, eng)

	ctx, cancel := context.WithCancel(context.Background())
	s := &server{
		listener: l,
		cancel:   cancel,
		addr:     l.Addr().(*net.TCPAddr),
		schema:   sch,
		engine:   eng,
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		_ = wire.Serve(ctx, l, wire.Deps{Schema: sch, Engine: eng})
	}()

	t.Cleanup(s.Stop)
	return s, nil
}

// bootstrapUsers registers the M1 hardcoded `users(id int4, name text)`
// table. M2 replaces this with parsed CREATE TABLE.
func bootstrapUsers(sch catalog.Schema, eng storage.Engine) {
	_ = sch.CreateTable(catalog.Table{
		Name: "users",
		Columns: []catalog.Column{
			{Name: "id", Type: types.Int4, NotNull: true},
			{Name: "name", Type: types.Text, NotNull: true},
		},
	})
	eng.CreateTable("users", 2)
}

type server struct {
	listener net.Listener
	cancel   context.CancelFunc
	addr     *net.TCPAddr

	schema catalog.Schema
	engine storage.Engine

	stopOnce sync.Once
	wg       sync.WaitGroup
}

func (s *server) DSN() string {
	return fmt.Sprintf(
		"postgres://pgmem@%s/pgmem?sslmode=disable",
		s.addr.String(),
	)
}

func (s *server) Stop() {
	s.stopOnce.Do(func() {
		s.cancel()
		_ = s.listener.Close()
		s.wg.Wait()
	})
}

func (s *server) SetNow(_ time.Time) {
	// Wired in M2 with the clock injection; no-op for M0/M1.
}

func (s *server) Seed(table string, rows ...[]any) error {
	t, ok := s.engine.Table(table)
	if !ok {
		return fmt.Errorf("seed: unknown table %q", table)
	}
	for _, r := range rows {
		t.Insert(storage.Row(r))
	}
	return nil
}
