package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func existsPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
		`CREATE TABLE users (id int PRIMARY KEY, name text NOT NULL)`,
		`CREATE TABLE orders (id int PRIMARY KEY, user_id int NOT NULL)`,
		`INSERT INTO users (id, name) VALUES (1, 'alice'), (2, 'bob'), (3, 'carol')`,
		`INSERT INTO orders (id, user_id) VALUES (10, 1), (11, 1), (12, 2)`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestExists_True(t *testing.T) {
	pool, ctx, cleanup := existsPool(t)
	defer cleanup()
	var got bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM users WHERE id = 1)`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !got {
		t.Errorf("got false, want true")
	}
}

func TestExists_False(t *testing.T) {
	pool, ctx, cleanup := existsPool(t)
	defer cleanup()
	var got bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM users WHERE id = 999)`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got {
		t.Errorf("got true, want false")
	}
}

func TestNotExists(t *testing.T) {
	pool, ctx, cleanup := existsPool(t)
	defer cleanup()
	var got bool
	if err := pool.QueryRow(ctx,
		`SELECT NOT EXISTS (SELECT 1 FROM users WHERE id = 999)`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !got {
		t.Errorf("got false, want true")
	}
}

func TestExists_AsFilter(t *testing.T) {
	pool, ctx, cleanup := existsPool(t)
	defer cleanup()
	// Users with at least one order. EXISTS is uncorrelated here so
	// it acts as a constant predicate — every user passes when the
	// inner plan finds rows.
	rows, err := pool.Query(ctx,
		`SELECT id FROM users WHERE EXISTS (SELECT 1 FROM orders WHERE user_id = 1) ORDER BY id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var ids []int32
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	want := []int32{1, 2, 3}
	if len(ids) != len(want) {
		t.Fatalf("got %v, want %v", ids, want)
	}
}
