package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func isNullPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
		`INSERT INTO t (id, n) VALUES (1, 10), (2, NULL), (3, 30), (4, NULL)`,
	); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func collectIDs(t *testing.T, pool *pgxpool.Pool, ctx context.Context, sql string, args ...any) []int32 {
	t.Helper()
	rows, err := pool.Query(ctx, sql, args...)
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

func sameIDs(a, b []int32) bool {
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

func TestIsNull_Filters(t *testing.T) {
	pool, ctx, cleanup := isNullPool(t)
	defer cleanup()
	got := collectIDs(t, pool, ctx, `SELECT id FROM t WHERE n IS NULL ORDER BY id`)
	if !sameIDs(got, []int32{2, 4}) {
		t.Errorf("IS NULL: got %v, want [2 4]", got)
	}
}

func TestIsNotNull_Filters(t *testing.T) {
	pool, ctx, cleanup := isNullPool(t)
	defer cleanup()
	got := collectIDs(t, pool, ctx, `SELECT id FROM t WHERE n IS NOT NULL ORDER BY id`)
	if !sameIDs(got, []int32{1, 3}) {
		t.Errorf("IS NOT NULL: got %v, want [1 3]", got)
	}
}

// `n = NULL` is always NULL (not true), so it returns no rows — only
// `IS NULL` matches NULL values. This guards against regressing into
// equality semantics.
func TestIsNull_DistinctFromEquals(t *testing.T) {
	pool, ctx, cleanup := isNullPool(t)
	defer cleanup()
	got := collectIDs(t, pool, ctx, `SELECT id FROM t WHERE n = NULL ORDER BY id`)
	if len(got) != 0 {
		t.Errorf("n = NULL: got %v, want []", got)
	}
}
