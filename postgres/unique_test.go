package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

// TestUniqueViolation_OverWire end-to-end-tests the UNIQUE path. Real
// PG returns SQLSTATE 23505 for a duplicate-key insert, which sqlc test
// suites pattern-match on. We do the same.
func TestUniqueViolation_OverWire(t *testing.T) {
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

	if _, err := pool.Exec(ctx, `CREATE TABLE accounts (id int PRIMARY KEY, name text NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name) VALUES ($1, $2)`, int32(1), "alice"); err != nil {
		t.Fatalf("first INSERT: %v", err)
	}

	_, err = pool.Exec(ctx, `INSERT INTO accounts (id, name) VALUES ($1, $2)`, int32(1), "alice-clone")
	if err == nil {
		t.Fatal("dup INSERT: want error, got nil")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("err type: got %T (%v), want *pgconn.PgError", err, err)
	}
	if pgErr.Code != "23505" {
		t.Errorf("SQLSTATE: got %q, want %q", pgErr.Code, "23505")
	}

	// Single original row should still be the only row in the table.
	var count int64
	if err := pool.QueryRow(ctx, `SELECT id FROM accounts ORDER BY id LIMIT 100`).Scan(&count); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if count != 1 {
		t.Errorf("survivor id: got %d, want 1", count)
	}
}

// TestUniqueViolation_PrimaryKeyAlsoEnforcesNotNull confirms that
// PRIMARY KEY in DDL gives BOTH 23502 (for NULL) and 23505 (for dup).
func TestUniqueViolation_PrimaryKeyAlsoEnforcesNotNull(t *testing.T) {
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

	if _, err := pool.Exec(ctx, `CREATE TABLE items (id int PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}

	_, err = pool.Exec(ctx, `INSERT INTO items (id) VALUES ($1)`, nil)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23502" {
		t.Errorf("NULL into PK: got %v, want 23502", err)
	}
}
