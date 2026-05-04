package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func aggDistinctPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
		`CREATE TABLE t (id int PRIMARY KEY, kind text NOT NULL, n int NOT NULL)`,
		`INSERT INTO t (id, kind, n) VALUES
			(1, 'a', 10),
			(2, 'a', 10),
			(3, 'b', 20),
			(4, 'b', 30),
			(5, 'a', 40)`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestAggDistinct_Count(t *testing.T) {
	pool, ctx, cleanup := aggDistinctPool(t)
	defer cleanup()
	var got int64
	if err := pool.QueryRow(ctx, `SELECT count(DISTINCT kind) FROM t`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != 2 {
		t.Errorf("got %d, want 2", got)
	}
}

func TestAggDistinct_CountVsTotal(t *testing.T) {
	pool, ctx, cleanup := aggDistinctPool(t)
	defer cleanup()
	var distinct, total int64
	if err := pool.QueryRow(ctx,
		`SELECT count(DISTINCT n), count(n) FROM t`,
	).Scan(&distinct, &total); err != nil {
		t.Fatalf("query: %v", err)
	}
	if distinct != 4 { // 10, 20, 30, 40
		t.Errorf("count(DISTINCT n) = %d, want 4", distinct)
	}
	if total != 5 {
		t.Errorf("count(n) = %d, want 5", total)
	}
}

func TestAggDistinct_Sum(t *testing.T) {
	pool, ctx, cleanup := aggDistinctPool(t)
	defer cleanup()
	var got int64
	if err := pool.QueryRow(ctx, `SELECT sum(DISTINCT n) FROM t`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != 100 { // 10 + 20 + 30 + 40 (the second 10 is dropped)
		t.Errorf("got %d, want 100", got)
	}
}

func TestAggDistinct_Grouped(t *testing.T) {
	pool, ctx, cleanup := aggDistinctPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx,
		`SELECT kind, count(DISTINCT n) FROM t GROUP BY kind ORDER BY kind`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	type pair struct {
		kind  string
		count int64
	}
	var got []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.kind, &p.count); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, p)
	}
	want := []pair{{"a", 2}, {"b", 2}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v, want %v", got, want)
	}
}
