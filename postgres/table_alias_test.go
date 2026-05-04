package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func aliasPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
		`INSERT INTO users (id, name) VALUES (1, 'alice'), (2, 'bob')`,
		`INSERT INTO orders (id, user_id, amount) VALUES (10, 1, 100), (11, 2, 50)`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestTableAlias_Implicit(t *testing.T) {
	pool, ctx, cleanup := aliasPool(t)
	defer cleanup()
	var name string
	if err := pool.QueryRow(ctx,
		`SELECT u.name FROM users u WHERE u.id = 1`,
	).Scan(&name); err != nil {
		t.Fatalf("query: %v", err)
	}
	if name != "alice" {
		t.Errorf("got %q, want alice", name)
	}
}

func TestTableAlias_AS(t *testing.T) {
	pool, ctx, cleanup := aliasPool(t)
	defer cleanup()
	var name string
	if err := pool.QueryRow(ctx,
		`SELECT u.name FROM users AS u WHERE u.id = 2`,
	).Scan(&name); err != nil {
		t.Fatalf("query: %v", err)
	}
	if name != "bob" {
		t.Errorf("got %q, want bob", name)
	}
}

func TestTableAlias_Join(t *testing.T) {
	pool, ctx, cleanup := aliasPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `
		SELECT u.name, o.amount
		FROM users u
		JOIN orders o ON u.id = o.user_id
		ORDER BY u.id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	type pair struct {
		name   string
		amount int32
	}
	var got []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.name, &p.amount); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, p)
	}
	want := []pair{{"alice", 100}, {"bob", 50}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %v, want %v", i, got[i], want[i])
		}
	}
}
