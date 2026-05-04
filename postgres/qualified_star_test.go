package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func qstarPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
		`CREATE TABLE orders (id int PRIMARY KEY, user_id int NOT NULL, amount int NOT NULL)`,
		`INSERT INTO users (id, name) VALUES (1, 'alice')`,
		`INSERT INTO orders (id, user_id, amount) VALUES (10, 1, 100)`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestQualifiedStar_AllFromOneSide(t *testing.T) {
	pool, ctx, cleanup := qstarPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `
		SELECT u.* FROM users u JOIN orders o ON u.id = o.user_id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	desc := rows.FieldDescriptions()
	if len(desc) != 2 {
		t.Errorf("got %d cols, want 2 (id, name)", len(desc))
	}
	if !rows.Next() {
		t.Fatal("expected a row")
	}
	var id int32
	var name string
	if err := rows.Scan(&id, &name); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if id != 1 || name != "alice" {
		t.Errorf("got id=%d name=%q, want 1/alice", id, name)
	}
}

func TestQualifiedStar_BothSides(t *testing.T) {
	pool, ctx, cleanup := qstarPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `
		SELECT u.*, o.* FROM users u JOIN orders o ON u.id = o.user_id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	desc := rows.FieldDescriptions()
	// users(id, name) + orders(id, user_id, amount) = 5 columns.
	if len(desc) != 5 {
		t.Errorf("got %d cols, want 5", len(desc))
	}
}

func TestQualifiedStar_MixedWithExpr(t *testing.T) {
	pool, ctx, cleanup := qstarPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `
		SELECT u.*, o.amount FROM users u JOIN orders o ON u.id = o.user_id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	desc := rows.FieldDescriptions()
	if len(desc) != 3 { // id, name, amount
		t.Errorf("got %d cols, want 3", len(desc))
	}
}
