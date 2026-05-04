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

func castPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

func TestCast_IntToText(t *testing.T) {
	pool, ctx, cleanup := castPool(t)
	defer cleanup()
	var got string
	if err := pool.QueryRow(ctx, `SELECT 42::text`).Scan(&got); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if got != "42" {
		t.Errorf("got %q, want 42", got)
	}
}

func TestCast_TextToInt(t *testing.T) {
	pool, ctx, cleanup := castPool(t)
	defer cleanup()
	var got int32
	if err := pool.QueryRow(ctx, `SELECT '42'::int`).Scan(&got); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if got != 42 {
		t.Errorf("got %d, want 42", got)
	}
}

// TestCast_Chains: x::int::text passes through both casts.
func TestCast_Chains(t *testing.T) {
	pool, ctx, cleanup := castPool(t)
	defer cleanup()
	var got string
	if err := pool.QueryRow(ctx, `SELECT '7'::int::text`).Scan(&got); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if got != "7" {
		t.Errorf("got %q, want 7", got)
	}
}

// TestCast_NullPropagates: NULL::T returns NULL with type T.
func TestCast_NullPropagates(t *testing.T) {
	pool, ctx, cleanup := castPool(t)
	defer cleanup()
	var got *string
	if err := pool.QueryRow(ctx, `SELECT NULL::text`).Scan(&got); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if got != nil {
		t.Errorf("got %q, want nil", *got)
	}
}

// TestCast_Int4ToInt8 widens.
func TestCast_Int4ToInt8(t *testing.T) {
	pool, ctx, cleanup := castPool(t)
	defer cleanup()
	var got int64
	if err := pool.QueryRow(ctx, `SELECT 1::bigint`).Scan(&got); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if got != 1 {
		t.Errorf("got %d, want 1", got)
	}
}

// TestCast_BadIntText: `'notanint'::int` should surface a runtime
// error wrapped as XX000.
func TestCast_BadIntText(t *testing.T) {
	pool, ctx, cleanup := castPool(t)
	defer cleanup()
	_, err := pool.Exec(ctx, `SELECT 'notanint'::int`)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("err type: got %T (%v), want *pgconn.PgError", err, err)
	}
}

// TestCast_TimestamptzText: now()::text formats a pinned clock value.
func TestCast_TimestamptzText(t *testing.T) {
	srv, err := postgres.Start(t)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	pinned := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	srv.SetNow(pinned)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, srv.DSN())
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	var got string
	if err := pool.QueryRow(ctx, `SELECT now()::text`).Scan(&got); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if got != "2025-01-02 03:04:05+00" {
		t.Errorf("got %q, want 2025-01-02 03:04:05+00", got)
	}
}
