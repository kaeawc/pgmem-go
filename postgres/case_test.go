package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func casePool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	if _, err := pool.Exec(ctx,
		`INSERT INTO t (id, n, label) VALUES (1, 10, 'a'), (2, 20, 'b'), (3, 30, 'c'), (4, NULL, 'd')`,
	); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

// TestCase_Searched: each WHEN is its own predicate; first match wins.
func TestCase_Searched(t *testing.T) {
	pool, ctx, cleanup := casePool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `
		SELECT id,
			CASE WHEN n < 15 THEN 'low'
			     WHEN n < 25 THEN 'mid'
			     ELSE 'high'
			END
		FROM t WHERE n IS NOT NULL ORDER BY id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	type pair struct {
		id  int32
		lbl string
	}
	var got []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.id, &p.lbl); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, p)
	}
	want := []pair{{1, "low"}, {2, "mid"}, {3, "high"}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %v, want %v", i, got[i], want[i])
		}
	}
}

// TestCase_Simple: `CASE expr WHEN val THEN ...` compares for equality.
func TestCase_Simple(t *testing.T) {
	pool, ctx, cleanup := casePool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `
		SELECT CASE label
		         WHEN 'a' THEN 1
		         WHEN 'b' THEN 2
		         ELSE 0
		       END
		FROM t ORDER BY id`)
	if err != nil {
		t.Fatalf("query: %v", err)
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
	want := []int32{1, 2, 0, 0}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %v, want %v", i, got[i], want[i])
		}
	}
}

// TestCase_NoElseDefaultsNull: when no WHEN matches and there is no
// ELSE, the result is NULL.
func TestCase_NoElseDefaultsNull(t *testing.T) {
	pool, ctx, cleanup := casePool(t)
	defer cleanup()
	var got *string
	if err := pool.QueryRow(ctx,
		`SELECT CASE WHEN 1 = 2 THEN 'never' END`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != nil {
		t.Errorf("got %q, want NULL", *got)
	}
}

// TestCase_NullOperand: in the simple form, NULL operand never equals
// anything (matches PG), so it falls through to ELSE.
func TestCase_NullOperand(t *testing.T) {
	pool, ctx, cleanup := casePool(t)
	defer cleanup()
	var got string
	if err := pool.QueryRow(ctx,
		`SELECT CASE n WHEN 10 THEN 'ten' ELSE 'other' END FROM t WHERE id = 4`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != "other" {
		t.Errorf("got %q, want %q", got, "other")
	}
}
