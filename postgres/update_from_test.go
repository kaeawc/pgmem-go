package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func updateFromPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
		`CREATE TABLE products (id int PRIMARY KEY, name text NOT NULL, price int NOT NULL)`,
		`CREATE TABLE pending_changes (product_id int PRIMARY KEY, new_price int NOT NULL)`,
		`INSERT INTO products (id, name, price) VALUES (1, 'a', 100), (2, 'b', 200), (3, 'c', 300)`,
		`INSERT INTO pending_changes (product_id, new_price) VALUES (1, 150), (3, 350)`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

// TestUpdateFrom_SimpleJoin updates the target via a join with an
// auxiliary table. Products 1 and 3 should pick up the staged price;
// product 2 stays at 200.
func TestUpdateFrom_SimpleJoin(t *testing.T) {
	pool, ctx, cleanup := updateFromPool(t)
	defer cleanup()
	tag, err := pool.Exec(ctx, `
		UPDATE products
		SET price = pending_changes.new_price
		FROM pending_changes
		WHERE products.id = pending_changes.product_id`)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if tag.RowsAffected() != 2 {
		t.Errorf("RowsAffected = %d, want 2", tag.RowsAffected())
	}
	rows, err := pool.Query(ctx, `SELECT id, price FROM products ORDER BY id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	got := map[int32]int32{}
	for rows.Next() {
		var id, price int32
		if err := rows.Scan(&id, &price); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[id] = price
	}
	want := map[int32]int32{1: 150, 2: 200, 3: 350}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("price[%d] = %d, want %d", id, got[id], w)
		}
	}
}

// TestUpdateFrom_Returning ensures RETURNING uses the post-update
// target row, ignoring the from-side.
func TestUpdateFrom_Returning(t *testing.T) {
	pool, ctx, cleanup := updateFromPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `
		UPDATE products SET price = pending_changes.new_price
		FROM pending_changes
		WHERE products.id = pending_changes.product_id
		RETURNING id, price`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	got := map[int32]int32{}
	for rows.Next() {
		var id, price int32
		if err := rows.Scan(&id, &price); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[id] = price
	}
	if got[1] != 150 || got[3] != 350 {
		t.Errorf("got %v", got)
	}
}

// TestDeleteUsing exercises `DELETE FROM t USING aux WHERE …`.
func TestDeleteUsing(t *testing.T) {
	pool, ctx, cleanup := updateFromPool(t)
	defer cleanup()
	tag, err := pool.Exec(ctx, `
		DELETE FROM products
		USING pending_changes
		WHERE products.id = pending_changes.product_id`)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if tag.RowsAffected() != 2 {
		t.Errorf("RowsAffected = %d, want 2", tag.RowsAffected())
	}
	var n int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM products`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("remaining rows = %d, want 1", n)
	}
}
