package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func nullsPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, n int)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO t (id, n) VALUES (1, 10), (2, NULL), (3, 30), (4, NULL), (5, 20)`,
	); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func collectIDsByQuery(t *testing.T, pool *pgxpool.Pool, ctx context.Context, sql string) []int32 {
	t.Helper()
	rows, err := pool.Query(ctx, sql)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	defer rows.Close()
	var got []int32
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	return got
}

func equal(a, b []int32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// PG default: ASC → NULLS LAST.
func TestNullsOrder_AscDefault(t *testing.T) {
	pool, ctx, cleanup := nullsPool(t)
	defer cleanup()
	got := collectIDsByQuery(t, pool, ctx, `SELECT id FROM t ORDER BY n ASC, id`)
	want := []int32{1, 5, 3, 2, 4}
	if !equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// PG default: DESC → NULLS FIRST.
func TestNullsOrder_DescDefault(t *testing.T) {
	pool, ctx, cleanup := nullsPool(t)
	defer cleanup()
	got := collectIDsByQuery(t, pool, ctx, `SELECT id FROM t ORDER BY n DESC, id`)
	want := []int32{2, 4, 3, 5, 1}
	if !equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestNullsOrder_AscNullsFirst(t *testing.T) {
	pool, ctx, cleanup := nullsPool(t)
	defer cleanup()
	got := collectIDsByQuery(t, pool, ctx, `SELECT id FROM t ORDER BY n ASC NULLS FIRST, id`)
	want := []int32{2, 4, 1, 5, 3}
	if !equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestNullsOrder_DescNullsLast(t *testing.T) {
	pool, ctx, cleanup := nullsPool(t)
	defer cleanup()
	got := collectIDsByQuery(t, pool, ctx, `SELECT id FROM t ORDER BY n DESC NULLS LAST, id`)
	want := []int32{3, 5, 1, 2, 4}
	if !equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
