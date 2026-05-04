package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func gsPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	return pool, ctx, func() { pool.Close(); cancel() }
}

func gsCollect(t *testing.T, pool *pgxpool.Pool, ctx context.Context, sql string, args ...any) []int64 {
	t.Helper()
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	return out
}

func TestGenerateSeries_Basic(t *testing.T) {
	pool, ctx, cleanup := gsPool(t)
	defer cleanup()
	got := gsCollect(t, pool, ctx, `SELECT * FROM generate_series(1, 5)`)
	want := []int64{1, 2, 3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] got %d, want %d", i, got[i], want[i])
		}
	}
}

func TestGenerateSeries_Step(t *testing.T) {
	pool, ctx, cleanup := gsPool(t)
	defer cleanup()
	got := gsCollect(t, pool, ctx, `SELECT * FROM generate_series(0, 10, 2)`)
	want := []int64{0, 2, 4, 6, 8, 10}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestGenerateSeries_NegativeStep(t *testing.T) {
	pool, ctx, cleanup := gsPool(t)
	defer cleanup()
	got := gsCollect(t, pool, ctx, `SELECT * FROM generate_series(5, 1, -1)`)
	want := []int64{5, 4, 3, 2, 1}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestGenerateSeries_Empty(t *testing.T) {
	pool, ctx, cleanup := gsPool(t)
	defer cleanup()
	got := gsCollect(t, pool, ctx, `SELECT * FROM generate_series(10, 1)`)
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestGenerateSeries_ZeroStep(t *testing.T) {
	pool, ctx, cleanup := gsPool(t)
	defer cleanup()
	if _, err := pool.Query(ctx, `SELECT * FROM generate_series(1, 5, 0)`); err == nil {
		t.Fatalf("expected error for zero step")
	}
}

func TestGenerateSeries_Alias(t *testing.T) {
	pool, ctx, cleanup := gsPool(t)
	defer cleanup()
	// Table alias only (column-list aliases like `AS gs(n)` are not
	// yet supported). The column name stays "generate_series".
	got := gsCollect(t, pool, ctx, `SELECT gs.generate_series FROM generate_series(1, 3) AS gs`)
	if len(got) != 3 {
		t.Fatalf("got %v, want 3 rows", got)
	}
}

func TestGenerateSeries_Joined(t *testing.T) {
	pool, ctx, cleanup := gsPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, name text)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, name) VALUES (1, 'a'), (2, 'b'), (3, 'c')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	rows, err := pool.Query(ctx, `SELECT t.name FROM t INNER JOIN generate_series(1, 3) gs ON gs.generate_series = t.id ORDER BY t.id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, s)
	}
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] got %s, want %s", i, got[i], want[i])
		}
	}
}

func TestGenerateSeries_Bigint(t *testing.T) {
	pool, ctx, cleanup := gsPool(t)
	defer cleanup()
	// Force int8 with a literal beyond int32 range.
	got := gsCollect(t, pool, ctx, `SELECT * FROM generate_series(2147483648, 2147483650)`)
	want := []int64{2147483648, 2147483649, 2147483650}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
