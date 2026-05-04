package postgres_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func unionPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
		`CREATE TABLE a (id int PRIMARY KEY, n int NOT NULL)`,
		`CREATE TABLE b (id int PRIMARY KEY, n int NOT NULL)`,
		`INSERT INTO a (id, n) VALUES (1, 10), (2, 20), (3, 30)`,
		`INSERT INTO b (id, n) VALUES (4, 30), (5, 40)`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func collectInts(t *testing.T, pool *pgxpool.Pool, ctx context.Context, sql string) []int32 {
	t.Helper()
	rows, err := pool.Query(ctx, sql)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
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
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	return got
}

func TestUnionAll_KeepsDuplicates(t *testing.T) {
	pool, ctx, cleanup := unionPool(t)
	defer cleanup()
	got := collectInts(t, pool, ctx, `SELECT n FROM a UNION ALL SELECT n FROM b`)
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	want := []int32{10, 20, 30, 30, 40}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestUnion_Deduplicates(t *testing.T) {
	pool, ctx, cleanup := unionPool(t)
	defer cleanup()
	got := collectInts(t, pool, ctx, `SELECT n FROM a UNION SELECT n FROM b`)
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	want := []int32{10, 20, 30, 40}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestUnion_Chain(t *testing.T) {
	pool, ctx, cleanup := unionPool(t)
	defer cleanup()
	got := collectInts(t, pool, ctx,
		`SELECT n FROM a UNION ALL SELECT n FROM b UNION ALL SELECT 99`)
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	want := []int32{10, 20, 30, 30, 40, 99}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestUnion_ColumnCountMismatch(t *testing.T) {
	pool, ctx, cleanup := unionPool(t)
	defer cleanup()
	_, err := pool.Query(ctx, `SELECT id, n FROM a UNION SELECT n FROM b`)
	if err == nil {
		t.Fatal("expected error on column-count mismatch")
	}
}
