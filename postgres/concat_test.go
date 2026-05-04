package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func concatPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

func TestConcat_Basic(t *testing.T) {
	pool, ctx, cleanup := concatPool(t)
	defer cleanup()
	var got string
	if err := pool.QueryRow(ctx, `SELECT concat('a', 'b', 'c')`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != "abc" {
		t.Errorf("got %q, want %q", got, "abc")
	}
}

// concat treats NULL as empty (unlike `||` which propagates NULL).
func TestConcat_NullsSkipped(t *testing.T) {
	pool, ctx, cleanup := concatPool(t)
	defer cleanup()
	var got string
	if err := pool.QueryRow(ctx, `SELECT concat('a', NULL, 'c')`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != "ac" {
		t.Errorf("got %q, want %q", got, "ac")
	}
}

func TestConcat_MixedTypes(t *testing.T) {
	pool, ctx, cleanup := concatPool(t)
	defer cleanup()
	var got string
	if err := pool.QueryRow(ctx, `SELECT concat('count: ', 42)`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != "count: 42" {
		t.Errorf("got %q, want %q", got, "count: 42")
	}
}

func TestConcatWs_Basic(t *testing.T) {
	pool, ctx, cleanup := concatPool(t)
	defer cleanup()
	var got string
	if err := pool.QueryRow(ctx, `SELECT concat_ws(', ', 'a', 'b', 'c')`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != "a, b, c" {
		t.Errorf("got %q, want %q", got, "a, b, c")
	}
}

func TestConcatWs_NullsSkippedNoLeadingSep(t *testing.T) {
	pool, ctx, cleanup := concatPool(t)
	defer cleanup()
	var got string
	if err := pool.QueryRow(ctx, `SELECT concat_ws('-', NULL, 'a', NULL, 'b')`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != "a-b" {
		t.Errorf("got %q, want %q", got, "a-b")
	}
}

func TestConcatWs_NullSepIsNull(t *testing.T) {
	pool, ctx, cleanup := concatPool(t)
	defer cleanup()
	var got *string
	if err := pool.QueryRow(ctx, `SELECT concat_ws(NULL::text, 'a', 'b')`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != nil {
		t.Errorf("got %q, want NULL", *got)
	}
}
