package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func aggPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, n int, label text)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, n, label) VALUES (1, 10, 'a'), (2, 20, 'b'), (3, 30, NULL), (4, NULL, 'd')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestAggregate_CountStar(t *testing.T) {
	pool, ctx, cleanup := aggPool(t)
	defer cleanup()
	var got int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&got); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if got != 4 {
		t.Errorf("got %d, want 4", got)
	}
}

// TestAggregate_CountColumn skips NULLs (PG semantics).
func TestAggregate_CountColumn(t *testing.T) {
	pool, ctx, cleanup := aggPool(t)
	defer cleanup()
	var got int64
	if err := pool.QueryRow(ctx, `SELECT count(label) FROM t`).Scan(&got); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if got != 3 {
		t.Errorf("count(label): got %d, want 3 (one row has NULL label)", got)
	}
}

func TestAggregate_SumMinMaxAvg(t *testing.T) {
	pool, ctx, cleanup := aggPool(t)
	defer cleanup()
	var sum, mn, mx, avg int64
	if err := pool.QueryRow(ctx,
		`SELECT sum(n), min(n), max(n), avg(n) FROM t`,
	).Scan(&sum, &mn, &mx, &avg); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	// Non-null n's are 10, 20, 30. Sum=60, Min=10, Max=30, Avg=20 (integer truncation).
	if sum != 60 || mn != 10 || mx != 30 || avg != 20 {
		t.Errorf("got sum=%d min=%d max=%d avg=%d, want 60/10/30/20", sum, mn, mx, avg)
	}
}

// TestAggregate_OnEmptyTable: count → 0, sum/min/max/avg → NULL.
func TestAggregate_OnEmptyTable(t *testing.T) {
	srv, err := postgres.Start(t)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, srv.DSN())
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `CREATE TABLE empty (n int)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}

	var c int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM empty`).Scan(&c); err != nil {
		t.Fatalf("count(*): %v", err)
	}
	if c != 0 {
		t.Errorf("count(*) on empty: got %d, want 0", c)
	}

	var sum, mn, mx, avg *int64
	if err := pool.QueryRow(ctx,
		`SELECT sum(n), min(n), max(n), avg(n) FROM empty`,
	).Scan(&sum, &mn, &mx, &avg); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if sum != nil || mn != nil || mx != nil || avg != nil {
		t.Errorf("got sum=%v min=%v max=%v avg=%v, want all nil", sum, mn, mx, avg)
	}
}

// TestAggregate_WithWhere: aggregates respect the WHERE filter.
func TestAggregate_WithWhere(t *testing.T) {
	pool, ctx, cleanup := aggPool(t)
	defer cleanup()
	var got int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM t WHERE n >= 20`).Scan(&got); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if got != 2 {
		t.Errorf("count(*) WHERE n >= 20: got %d, want 2", got)
	}
}

// TestAggregate_MaxOnText: MAX/MIN polymorphic over text.
func TestAggregate_MaxOnText(t *testing.T) {
	pool, ctx, cleanup := aggPool(t)
	defer cleanup()
	var got string
	if err := pool.QueryRow(ctx, `SELECT max(label) FROM t`).Scan(&got); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if got != "d" {
		t.Errorf("max(label): got %q, want d", got)
	}
}

// TestAggregate_MixedRequiresGroupBy: aggregate + non-aggregate
// without GROUP BY surfaces a clear parse error rather than silently
// producing wrong rows.
func TestAggregate_MixedRequiresGroupBy(t *testing.T) {
	pool, ctx, cleanup := aggPool(t)
	defer cleanup()
	_, err := pool.Exec(ctx, `SELECT id, count(*) FROM t`)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("err type: got %T (%v), want *pgconn.PgError", err, err)
	}
}
