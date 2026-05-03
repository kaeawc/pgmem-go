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

// TestCheckViolation_OverWire is the end-to-end check-constraint flow:
// a CHECK in CREATE TABLE rejects a bad INSERT with SQLSTATE 23514, and
// passes a good one through.
func TestCheckViolation_OverWire(t *testing.T) {
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

	if _, err := pool.Exec(ctx, `CREATE TABLE inventory (id int PRIMARY KEY, qty int NOT NULL CHECK (qty > 0))`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO inventory (id, qty) VALUES ($1, $2)`, int32(1), int32(5)); err != nil {
		t.Fatalf("good INSERT: %v", err)
	}

	_, err = pool.Exec(ctx, `INSERT INTO inventory (id, qty) VALUES ($1, $2)`, int32(2), int32(0))
	if err == nil {
		t.Fatal("bad INSERT: want error, got nil")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("err type: got %T (%v), want *pgconn.PgError", err, err)
	}
	if pgErr.Code != "23514" {
		t.Errorf("SQLSTATE: got %q, want %q", pgErr.Code, "23514")
	}

	// Only the first (good) row survived.
	var n int32
	if err := pool.QueryRow(ctx, `SELECT id FROM inventory ORDER BY id`).Scan(&n); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if n != 1 {
		t.Errorf("survivor: got id=%d, want 1", n)
	}
}
