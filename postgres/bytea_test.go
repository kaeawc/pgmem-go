package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

// TestBytea_RoundTrip stores and reads back a non-trivial byte slice
// through Bind → INSERT → SELECT → Scan.
func TestBytea_RoundTrip(t *testing.T) {
	srv, err := postgres.Start(t)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, srv.DSN())
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, `CREATE TABLE blobs (id int PRIMARY KEY, body bytea NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	want := []byte{0, 1, 2, 0xfe, 0xff, 0x00, 'h', 'i'}
	if _, err := pool.Exec(ctx, `INSERT INTO blobs (id, body) VALUES ($1, $2)`, int32(1), want); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	var got []byte
	if err := pool.QueryRow(ctx, `SELECT body FROM blobs WHERE id = $1`, int32(1)).Scan(&got); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestBytea_UniqueConstraint exercises the checkUnique fix that maps
// non-comparable values through uniqueKey. Without it, inserting a
// second []byte would panic on the map lookup.
func TestBytea_UniqueConstraint(t *testing.T) {
	srv, err := postgres.Start(t)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, srv.DSN())
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, `CREATE TABLE keyed (k bytea PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	first := []byte{1, 2, 3}
	if _, err := pool.Exec(ctx, `INSERT INTO keyed (k) VALUES ($1)`, first); err != nil {
		t.Fatalf("first INSERT: %v", err)
	}
	// Same bytes — must collide.
	_, err = pool.Exec(ctx, `INSERT INTO keyed (k) VALUES ($1)`, first)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Errorf("dup: got %v, want 23505", err)
	}
	// Different bytes — fine.
	if _, err := pool.Exec(ctx, `INSERT INTO keyed (k) VALUES ($1)`, []byte{4, 5}); err != nil {
		t.Errorf("distinct: got %v, want success", err)
	}
}
