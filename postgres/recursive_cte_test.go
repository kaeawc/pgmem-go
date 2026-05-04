package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func recCTEPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

func TestRecursiveCTE_CountToTen(t *testing.T) {
	pool, ctx, cleanup := recCTEPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `
		WITH RECURSIVE t AS (
		  SELECT 1 AS n
		  UNION ALL
		  SELECT n + 1 FROM t WHERE n < 10
		)
		SELECT n FROM t ORDER BY n
	`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var got []int32
	for rows.Next() {
		var n int32
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	want := []int32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %d, want %d", i, got[i], want[i])
		}
	}
}

func TestRecursiveCTE_HierarchyWalk(t *testing.T) {
	pool, ctx, cleanup := recCTEPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE org (
		  id        bigint PRIMARY KEY,
		  parent_id bigint,
		  name      text NOT NULL
		)
	`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO org (id, parent_id, name) VALUES
		  (1, NULL, 'root'),
		  (2, 1, 'a'),
		  (3, 1, 'b'),
		  (4, 2, 'a1'),
		  (5, 2, 'a2'),
		  (6, 3, 'b1')
	`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	rows, err := pool.Query(ctx, `
		WITH RECURSIVE descendants AS (
		  SELECT id, parent_id, name FROM org WHERE id = 1
		  UNION ALL
		  SELECT o.id, o.parent_id, o.name
		    FROM org o INNER JOIN descendants d ON o.parent_id = d.id
		)
		SELECT name FROM descendants ORDER BY id
	`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, n)
	}
	want := []string{"root", "a", "b", "a1", "a2", "b1"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %s, want %s", i, got[i], want[i])
		}
	}
}

func TestRecursiveCTE_UnionDedups(t *testing.T) {
	pool, ctx, cleanup := recCTEPool(t)
	defer cleanup()
	// UNION (without ALL) should dedup. The recursive part keeps
	// emitting the same value, but the dedup check should stop the
	// iteration after the first new row appears once.
	rows, err := pool.Query(ctx, `
		WITH RECURSIVE t AS (
		  SELECT 1 AS n
		  UNION
		  SELECT 1 FROM t
		)
		SELECT n FROM t
	`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var n int32
	count := 0
	for rows.Next() {
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		count++
	}
	if count != 1 {
		t.Errorf("UNION dedup got %d rows, want 1", count)
	}
}

func TestRecursiveCTE_OuterReferencesAreFine(t *testing.T) {
	pool, ctx, cleanup := recCTEPool(t)
	defer cleanup()
	// Recursive CTE result is consumable like a normal CTE in the
	// outer WHERE clause.
	rows, err := pool.Query(ctx, `
		WITH RECURSIVE t AS (
		  SELECT 1 AS n
		  UNION ALL
		  SELECT n + 1 FROM t WHERE n < 5
		)
		SELECT n FROM t WHERE n > 2 ORDER BY n
	`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var got []int32
	for rows.Next() {
		var n int32
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, n)
	}
	want := []int32{3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %d, want %d", i, got[i], want[i])
		}
	}
}
