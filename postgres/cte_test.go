package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func ctePool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
		`CREATE TABLE orders (id int PRIMARY KEY, user_id int NOT NULL, amount int NOT NULL)`,
		`INSERT INTO orders (id, user_id, amount) VALUES
			(1, 1, 10),
			(2, 1, 20),
			(3, 2, 5),
			(4, 3, 100)`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestCTE_Simple(t *testing.T) {
	pool, ctx, cleanup := ctePool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `
		WITH big AS (SELECT id, amount FROM orders WHERE amount > 15)
		SELECT id FROM big ORDER BY id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var ids []int32
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	want := []int32{2, 4}
	if len(ids) != len(want) || ids[0] != want[0] || ids[1] != want[1] {
		t.Errorf("got %v, want %v", ids, want)
	}
}

func TestCTE_Multiple(t *testing.T) {
	pool, ctx, cleanup := ctePool(t)
	defer cleanup()
	// Second CTE references the first.
	rows, err := pool.Query(ctx, `
		WITH big AS (SELECT id, amount FROM orders WHERE amount > 5),
		     huge AS (SELECT id FROM big WHERE amount > 50)
		SELECT id FROM huge ORDER BY id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var ids []int32
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 1 || ids[0] != 4 {
		t.Errorf("got %v, want [4]", ids)
	}
}

func TestCTE_AggregateFromCTE(t *testing.T) {
	pool, ctx, cleanup := ctePool(t)
	defer cleanup()
	var totalUsers int64
	if err := pool.QueryRow(ctx, `
		WITH per_user AS (SELECT user_id, sum(amount) AS total FROM orders GROUP BY user_id)
		SELECT count(*) FROM per_user WHERE total > 25`,
	).Scan(&totalUsers); err != nil {
		t.Fatalf("query: %v", err)
	}
	if totalUsers != 2 {
		t.Errorf("got %d, want 2", totalUsers)
	}
}

func TestCTE_ReferencedTwice(t *testing.T) {
	pool, ctx, cleanup := ctePool(t)
	defer cleanup()
	// Reference the same CTE twice via UNION ALL — each reference
	// re-runs the plan, which is fine for our v0.
	rows, err := pool.Query(ctx, `
		WITH small AS (SELECT id FROM orders WHERE amount < 50)
		SELECT id FROM small UNION ALL SELECT id FROM small`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var ids []int32
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 6 { // 3 small rows × 2 references
		t.Errorf("got %d rows, want 6", len(ids))
	}
}
