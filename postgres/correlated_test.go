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
