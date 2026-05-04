package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func boolAggPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	for _, sql := range []string{
		`CREATE TABLE flags (id int PRIMARY KEY, kind text NOT NULL, ok bool)`,
		`INSERT INTO flags (id, kind, ok) VALUES
			(1, 'a', true),
			(2, 'a', true),
			(3, 'b', false),
			(4, 'b', true),
			(5, 'c', NULL)`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestBoolAnd_AllTrue(t *testing.T) {
	pool, ctx, cleanup := boolAggPool(t)
	defer cleanup()
	var got bool
	if err := pool.QueryRow(ctx,
		`SELECT bool_and(ok) FROM flags WHERE kind = 'a'`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !got {
		t.Errorf("got false, want true")
	}
}

func TestBoolAnd_OneFalse(t *testing.T) {
	pool, ctx, cleanup := boolAggPool(t)
	defer cleanup()
	var got bool
	if err := pool.QueryRow(ctx,
		`SELECT bool_and(ok) FROM flags WHERE kind = 'b'`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got {
		t.Errorf("got true, want false")
	}
}

func TestBoolOr_AnyTrue(t *testing.T) {
	pool, ctx, cleanup := boolAggPool(t)
	defer cleanup()
	var got bool
	if err := pool.QueryRow(ctx,
		`SELECT bool_or(ok) FROM flags WHERE kind = 'b'`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !got {
		t.Errorf("got false, want true")
	}
}

func TestBoolAgg_AllNullsIsNull(t *testing.T) {
	pool, ctx, cleanup := boolAggPool(t)
	defer cleanup()
	var got *bool
	if err := pool.QueryRow(ctx,
		`SELECT bool_and(ok) FROM flags WHERE kind = 'c'`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != nil {
		t.Errorf("got %v, want NULL", *got)
	}
}

func TestEvery_SynonymForBoolAnd(t *testing.T) {
	pool, ctx, cleanup := boolAggPool(t)
	defer cleanup()
	var got bool
	if err := pool.QueryRow(ctx,
		`SELECT every(ok) FROM flags WHERE kind = 'a'`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !got {
		t.Errorf("got false, want true")
	}
}
