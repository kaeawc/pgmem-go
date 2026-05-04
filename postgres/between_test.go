package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func betweenPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, n int NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO t (id, n) VALUES (1, 10), (2, 20), (3, 30), (4, 40), (5, 50)`,
	); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func queryIDs(t *testing.T, pool *pgxpool.Pool, ctx context.Context, sql string, args ...any) []int32 {
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

func eqIDs(a, b []int32) bool {
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

func TestBetween_Inclusive(t *testing.T) {
	pool, ctx, cleanup := betweenPool(t)
	defer cleanup()
	got := queryIDs(t, pool, ctx, `SELECT id FROM t WHERE n BETWEEN 20 AND 40 ORDER BY id`)
	if !eqIDs(got, []int32{2, 3, 4}) {
		t.Errorf("BETWEEN 20 AND 40: got %v, want [2 3 4]", got)
	}
}

func TestNotBetween(t *testing.T) {
	pool, ctx, cleanup := betweenPool(t)
	defer cleanup()
	got := queryIDs(t, pool, ctx, `SELECT id FROM t WHERE n NOT BETWEEN 20 AND 40 ORDER BY id`)
	if !eqIDs(got, []int32{1, 5}) {
		t.Errorf("NOT BETWEEN 20 AND 40: got %v, want [1 5]", got)
	}
}

func TestBetween_Params(t *testing.T) {
	pool, ctx, cleanup := betweenPool(t)
	defer cleanup()
	got := queryIDs(t, pool, ctx,
		`SELECT id FROM t WHERE n BETWEEN $1 AND $2 ORDER BY id`,
		int32(15), int32(35),
	)
	if !eqIDs(got, []int32{2, 3}) {
		t.Errorf("BETWEEN $1 AND $2: got %v, want [2 3]", got)
	}
}

func TestBetween_Combined(t *testing.T) {
	pool, ctx, cleanup := betweenPool(t)
	defer cleanup()
	got := queryIDs(t, pool, ctx,
		`SELECT id FROM t WHERE n BETWEEN 10 AND 50 AND id > 2 ORDER BY id`,
	)
	if !eqIDs(got, []int32{3, 4, 5}) {
		t.Errorf("BETWEEN ... AND id > 2: got %v, want [3 4 5]", got)
	}
}
