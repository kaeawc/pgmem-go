package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func settingPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
	t.Helper()
	srv, err := postgres.Start(t)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	pool, err := pgxpool.New(ctx, srv.DSN())
	if err != nil {
		cancel()
		t.Fatalf("pgxpool.New: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestCurrentSetting_Known(t *testing.T) {
	pool, ctx, cleanup := settingPool(t)
	defer cleanup()
	cases := []struct {
		name, want string
	}{
		{"server_version", "16.0"},
		{"search_path", "public"},
		{"timezone", "UTC"},
		{"client_encoding", "UTF8"},
	}
	for _, c := range cases {
		var got string
		if err := pool.QueryRow(ctx, `SELECT current_setting($1)`, c.name).Scan(&got); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCurrentSetting_UnknownErrors(t *testing.T) {
	pool, ctx, cleanup := settingPool(t)
	defer cleanup()
	var got string
	err := pool.QueryRow(ctx, `SELECT current_setting('does_not_exist')`).Scan(&got)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42704" {
		t.Errorf("unknown: got %v, want SQLSTATE 42704", err)
	}
}

func TestCurrentSetting_MissingOK(t *testing.T) {
	pool, ctx, cleanup := settingPool(t)
	defer cleanup()
	var got *string
	if err := pool.QueryRow(ctx,
		`SELECT current_setting('does_not_exist', true)`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != nil {
		t.Errorf("got %q, want NULL", *got)
	}
}

func TestVersion(t *testing.T) {
	pool, ctx, cleanup := settingPool(t)
	defer cleanup()
	var got string
	if err := pool.QueryRow(ctx, `SELECT version()`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !strings.Contains(got, "PostgreSQL") || !strings.Contains(got, "pgmem") {
		t.Errorf("got %q, want a PostgreSQL/pgmem-style string", got)
	}
}
