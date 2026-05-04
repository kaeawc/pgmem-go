package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func correlatedPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
		`CREATE TABLE parents (id int PRIMARY KEY, name text NOT NULL)`,
		`CREATE TABLE children (id int PRIMARY KEY, parent_id int NOT NULL REFERENCES parents(id))`,
		`INSERT INTO parents (id, name) VALUES (1, 'a'), (2, 'b'), (3, 'c')`,
		`INSERT INTO children (id, parent_id) VALUES (10, 1), (11, 2)`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

// TestCorrelatedExists_FiltersByOuterRow exercises the basic shape:
// the inner SELECT references `p.id` from the outer FROM.
func TestCorrelatedExists_FiltersByOuterRow(t *testing.T) {
	pool, ctx, cleanup := correlatedPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `
		SELECT p.id FROM parents p
		WHERE EXISTS (SELECT 1 FROM children c WHERE c.parent_id = p.id)
		ORDER BY p.id`)
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
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("got %v, want [1 2]", got)
	}
}

// TestCorrelatedExists_NotExists also covers the negation path.
func TestCorrelatedExists_NotExists(t *testing.T) {
	pool, ctx, cleanup := correlatedPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `
		SELECT p.id FROM parents p
		WHERE NOT EXISTS (SELECT 1 FROM children c WHERE c.parent_id = p.id)
		ORDER BY p.id`)
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
	if len(got) != 1 || got[0] != 3 {
		t.Errorf("got %v, want [3]", got)
	}
}

// TestCorrelatedScalarSubquery exercises a `(SELECT count(*) FROM
// children WHERE c.parent_id = p.id)` projection: the inner query
// references the outer FROM's row.
func TestCorrelatedScalarSubquery(t *testing.T) {
	pool, ctx, cleanup := correlatedPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `
		SELECT p.id,
		       (SELECT count(*) FROM children c WHERE c.parent_id = p.id) AS n
		FROM parents p ORDER BY p.id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	type pair struct {
		id int32
		n  int64
	}
	var got []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.id, &p.n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, p)
	}
	want := []pair{{1, 1}, {2, 1}, {3, 0}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestCorrelatedScalarSubquery_NULLOnEmpty confirms that an empty
// inner result yields a NULL value (not the aggregate's zero).
func TestCorrelatedScalarSubquery_NULLOnEmpty(t *testing.T) {
	pool, ctx, cleanup := correlatedPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `
		SELECT p.id,
		       (SELECT c.id FROM children c WHERE c.parent_id = p.id LIMIT 1) AS first_child
		FROM parents p ORDER BY p.id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	type pair struct {
		id    int32
		child *int32
	}
	var got []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.id, &p.child); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, p)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3", len(got))
	}
	if got[0].child == nil || *got[0].child != 10 {
		t.Errorf("[0] child = %v, want 10", got[0].child)
	}
	if got[2].child != nil {
		t.Errorf("[2] should be NULL (no child), got %v", *got[2].child)
	}
}

// TestUncorrelatedExists_StillFastPath confirms the uncorrelated
// case still pre-evaluates to a constant (no per-row plan rebuild).
func TestUncorrelatedExists_StillFastPath(t *testing.T) {
	pool, ctx, cleanup := correlatedPool(t)
	defer cleanup()
	var got bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM children WHERE id = 99)`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got {
		t.Errorf("got true, want false")
	}
}
