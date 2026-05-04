package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func derivedPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
		`CREATE TABLE orders (id int PRIMARY KEY, user_id int NOT NULL, amount int NOT NULL)`,
		`INSERT INTO orders (id, user_id, amount) VALUES (1, 1, 10), (2, 1, 20), (3, 2, 5), (4, 2, 50)`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestDerivedTable_Basic(t *testing.T) {
	pool, ctx, cleanup := derivedPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `
		SELECT sub.amount FROM (SELECT amount FROM orders WHERE user_id = 1) sub
		ORDER BY sub.amount`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var got []int32
	for rows.Next() {
		var v int32
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, v)
	}
	want := []int32{10, 20}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDerivedTable_AS(t *testing.T) {
	pool, ctx, cleanup := derivedPool(t)
	defer cleanup()
	var got int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM (SELECT amount FROM orders WHERE amount > 5) AS big`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != 3 {
		t.Errorf("count = %d, want 3", got)
	}
}

func TestDerivedTable_AggregateOverSubquery(t *testing.T) {
	pool, ctx, cleanup := derivedPool(t)
	defer cleanup()
	// Sum of per-user totals.
	var got int64
	if err := pool.QueryRow(ctx, `
		SELECT sum(per_user.total) FROM
			(SELECT user_id, sum(amount) AS total FROM orders GROUP BY user_id) per_user`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != 85 { // 10+20+5+50
		t.Errorf("got %d, want 85", got)
	}
}

func TestDerivedTable_RequiresAlias(t *testing.T) {
	pool, ctx, cleanup := derivedPool(t)
	defer cleanup()
	_, err := pool.Query(ctx, `SELECT * FROM (SELECT * FROM orders)`)
	if err == nil {
		t.Fatal("expected error for missing alias on derived table")
	}
}
