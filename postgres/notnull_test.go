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

// TestNotNullViolation_OverWire is the end-to-end check that a NOT NULL
// failure surfaces on the wire as a PG SQLSTATE 23502 error — pgx
// surfaces this as a *pgconn.PgError, which is what sqlc-generated code
// inspects.
func TestNotNullViolation_OverWire(t *testing.T) {
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

	if _, err := pool.Exec(ctx, `CREATE TABLE widgets (id int NOT NULL, label text NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}

	// pgx encodes a Go nil as SQL NULL, so this INSERT puts NULL into the
	// label column, which is declared NOT NULL.
	_, err = pool.Exec(ctx, `INSERT INTO widgets (id, label) VALUES ($1, $2)`, int32(1), nil)
	if err == nil {
		t.Fatal("INSERT: want error, got nil")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("err type: got %T (%v), want *pgconn.PgError", err, err)
	}
	if pgErr.Code != "23502" {
		t.Errorf("SQLSTATE: got %q, want %q", pgErr.Code, "23502")
	}

	// And the table is still empty after the rejected insert.
	var n int64
	if err := pool.QueryRow(ctx, `SELECT id FROM widgets LIMIT 1`).Scan(&n); err == nil {
		t.Errorf("expected no rows; got id=%d", n)
	}
}
