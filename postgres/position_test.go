package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func positionPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

func TestPosition_Found(t *testing.T) {
	pool, ctx, cleanup := positionPool(t)
	defer cleanup()
	var got int32
	if err := pool.QueryRow(ctx,
		`SELECT position('lo' IN 'hello world')`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != 4 {
		t.Errorf("got %d, want 4", got)
	}
}

func TestPosition_NotFound(t *testing.T) {
	pool, ctx, cleanup := positionPool(t)
	defer cleanup()
	var got int32
	if err := pool.QueryRow(ctx,
		`SELECT position('xyz' IN 'hello world')`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

func TestPosition_NullPropagates(t *testing.T) {
	pool, ctx, cleanup := positionPool(t)
	defer cleanup()
	var got *int32
	if err := pool.QueryRow(ctx,
		`SELECT position(NULL::text IN 'hello')`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != nil {
		t.Errorf("got %d, want NULL", *got)
	}
}

func TestPosition_Unicode(t *testing.T) {
	pool, ctx, cleanup := positionPool(t)
	defer cleanup()
	var got int32
	// "é" precedes "llo". 1-indexed character position.
	if err := pool.QueryRow(ctx,
		`SELECT position('llo' IN 'héllo')`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != 3 {
		t.Errorf("got %d, want 3", got)
	}
}
