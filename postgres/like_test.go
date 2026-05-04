package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func likePool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, name text NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO t (id, name) VALUES (1, 'Alice'), (2, 'Bob'), (3, 'alfred'), (4, 'Carol')`,
	); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func selectIDs(t *testing.T, pool *pgxpool.Pool, ctx context.Context, sql string, args ...any) []int32 {
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

func equalIDs(a, b []int32) bool {
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

func TestLike_Prefix(t *testing.T) {
	pool, ctx, cleanup := likePool(t)
	defer cleanup()
	got := selectIDs(t, pool, ctx, `SELECT id FROM t WHERE name LIKE 'A%' ORDER BY id`)
	if !equalIDs(got, []int32{1}) {
		t.Errorf("LIKE 'A%%': got %v, want [1]", got)
	}
}

func TestLike_Underscore(t *testing.T) {
	pool, ctx, cleanup := likePool(t)
	defer cleanup()
	got := selectIDs(t, pool, ctx, `SELECT id FROM t WHERE name LIKE 'B_b' ORDER BY id`)
	if !equalIDs(got, []int32{2}) {
		t.Errorf("LIKE 'B_b': got %v, want [2]", got)
	}
}

func TestILike_CaseInsensitive(t *testing.T) {
	pool, ctx, cleanup := likePool(t)
	defer cleanup()
	got := selectIDs(t, pool, ctx, `SELECT id FROM t WHERE name ILIKE 'a%' ORDER BY id`)
	if !equalIDs(got, []int32{1, 3}) {
		t.Errorf("ILIKE 'a%%': got %v, want [1 3]", got)
	}
}

func TestLike_NotLike(t *testing.T) {
	pool, ctx, cleanup := likePool(t)
	defer cleanup()
	got := selectIDs(t, pool, ctx, `SELECT id FROM t WHERE name NOT LIKE 'A%' ORDER BY id`)
	if !equalIDs(got, []int32{2, 3, 4}) {
		t.Errorf("NOT LIKE 'A%%': got %v, want [2 3 4]", got)
	}
}

func TestLike_ParamPattern(t *testing.T) {
	pool, ctx, cleanup := likePool(t)
	defer cleanup()
	got := selectIDs(t, pool, ctx, `SELECT id FROM t WHERE name LIKE $1 ORDER BY id`, "%o%")
	if !equalIDs(got, []int32{2, 4}) {
		t.Errorf("LIKE $1='%%o%%': got %v, want [2 4]", got)
	}
}

func TestLike_EscapedPercent(t *testing.T) {
	pool, ctx, cleanup := likePool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, name) VALUES (5, '50%off')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	got := selectIDs(t, pool, ctx, `SELECT id FROM t WHERE name LIKE '50\%off' ORDER BY id`)
	if !equalIDs(got, []int32{5}) {
		t.Errorf("LIKE '50\\%%off': got %v, want [5]", got)
	}
}
