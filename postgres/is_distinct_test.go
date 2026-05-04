package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func isDistinctPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
		`INSERT INTO t (id, n) VALUES (1, 10), (2, NULL), (3, 30), (4, NULL), (5, 30)`,
	); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestIsDistinct_Truth(t *testing.T) {
	pool, ctx, cleanup := isDistinctPool(t)
	defer cleanup()
	cases := []struct {
		sql  string
		want bool
	}{
		{`SELECT 1 IS DISTINCT FROM 1`, false},
		{`SELECT 1 IS DISTINCT FROM 2`, true},
		{`SELECT NULL IS DISTINCT FROM NULL`, false},
		{`SELECT NULL IS DISTINCT FROM 1`, true},
		{`SELECT 1 IS NOT DISTINCT FROM NULL`, false},
		{`SELECT NULL IS NOT DISTINCT FROM NULL`, true},
	}
	for _, c := range cases {
		var got bool
		if err := pool.QueryRow(ctx, c.sql).Scan(&got); err != nil {
			t.Fatalf("%s: %v", c.sql, err)
		}
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.sql, got, c.want)
		}
	}
}

// `n IS DISTINCT FROM 30` filters out rows where n equals 30. A NULL
// `n` is distinct from 30 (so it matches), unlike `n != 30` which
// would be NULL → not-included.
func TestIsDistinct_FilterIncludesNulls(t *testing.T) {
	pool, ctx, cleanup := isDistinctPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx,
		`SELECT id FROM t WHERE n IS DISTINCT FROM 30 ORDER BY id`)
	if err != nil {
		t.Fatalf("query: %v", err)
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
	want := []int32{1, 2, 4}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %d, want %d", i, got[i], want[i])
		}
	}
}

// `n IS NOT DISTINCT FROM NULL` is the NULL-safe equality form. It
// matches rows where n is NULL.
func TestIsNotDistinct_NullSafeEquality(t *testing.T) {
	pool, ctx, cleanup := isDistinctPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx,
		`SELECT id FROM t WHERE n IS NOT DISTINCT FROM NULL ORDER BY id`)
	if err != nil {
		t.Fatalf("query: %v", err)
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
	want := []int32{2, 4}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v, want %v", got, want)
	}
}
