package postgres_test

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func cascadeSetup(t *testing.T, fkClause string) (*pgxpool.Pool, context.Context, func()) {
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
	if _, err := pool.Exec(ctx, `CREATE TABLE customers (id int PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE customers: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE orders (id int PRIMARY KEY, customer_id int `+fkClause+`)`); err != nil {
		t.Fatalf("CREATE orders: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO customers (id) VALUES (1), (2)`); err != nil {
		t.Fatalf("seed customers: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO orders (id, customer_id) VALUES (10, 1), (11, 1), (12, 2)`); err != nil {
		t.Fatalf("seed orders: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func orderCustomerIDs(t *testing.T, pool *pgxpool.Pool, ctx context.Context) []any {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT customer_id FROM orders ORDER BY id`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()
	var out []any
	for rows.Next() {
		var c *int32
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		if c == nil {
			out = append(out, nil)
		} else {
			out = append(out, *c)
		}
	}
	return out
}

// TestFK_OnDeleteCascade: deleting a parent removes all matching
// child rows automatically.
func TestFK_OnDeleteCascade(t *testing.T) {
	pool, ctx, cleanup := cascadeSetup(t, `NOT NULL REFERENCES customers(id) ON DELETE CASCADE`)
	defer cleanup()

	if _, err := pool.Exec(ctx, `DELETE FROM customers WHERE id = 1`); err != nil {
		t.Fatalf("DELETE customer: %v", err)
	}
	got := orderCustomerIDs(t, pool, ctx)
	if len(got) != 1 || got[0] != int32(2) {
		t.Errorf("orders after cascade: got %v, want [2] (only customer-2's order survives)", got)
	}
}

// TestFK_OnDeleteSetNull: deleting a parent nulls out matching
// dependent rows' FK columns instead of removing them.
func TestFK_OnDeleteSetNull(t *testing.T) {
	pool, ctx, cleanup := cascadeSetup(t, `REFERENCES customers(id) ON DELETE SET NULL`)
	defer cleanup()

	if _, err := pool.Exec(ctx, `DELETE FROM customers WHERE id = 1`); err != nil {
		t.Fatalf("DELETE customer: %v", err)
	}
	got := orderCustomerIDs(t, pool, ctx)
	// Three orders survive: rows 10/11 have NULL customer_id, row 12 still 2.
	sort.Slice(got, func(i, j int) bool {
		// nils first
		if got[i] == nil {
			return true
		}
		if got[j] == nil {
			return false
		}
		return got[i].(int32) < got[j].(int32)
	})
	if len(got) != 3 || got[0] != nil || got[1] != nil || got[2] != int32(2) {
		t.Errorf("orders after SET NULL: got %v, want [nil nil 2]", got)
	}
}

// TestFK_DefaultIsRestrict: omitting ON DELETE keeps RESTRICT
// behaviour from the previous slice.
func TestFK_DefaultIsRestrict(t *testing.T) {
	pool, ctx, cleanup := cascadeSetup(t, `NOT NULL REFERENCES customers(id)`)
	defer cleanup()

	_, err := pool.Exec(ctx, `DELETE FROM customers WHERE id = 1`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Errorf("got %v, want 23503", err)
	}
	// Nothing was deleted from either table.
	var n int32
	if err := pool.QueryRow(ctx, `SELECT id FROM customers WHERE id = 1`).Scan(&n); err != nil {
		t.Errorf("parent gone: %v", err)
	}
}

// TestFK_OnDeleteCascade_Recursive: cascades chain through grandchild
// tables.
func TestFK_OnDeleteCascade_Recursive(t *testing.T) {
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

	for _, sql := range []string{
		`CREATE TABLE a (id int PRIMARY KEY)`,
		`CREATE TABLE b (id int PRIMARY KEY, a_id int NOT NULL REFERENCES a(id) ON DELETE CASCADE)`,
		`CREATE TABLE c (id int PRIMARY KEY, b_id int NOT NULL REFERENCES b(id) ON DELETE CASCADE)`,
		`INSERT INTO a (id) VALUES (1)`,
		`INSERT INTO b (id, a_id) VALUES (10, 1)`,
		`INSERT INTO c (id, b_id) VALUES (100, 10)`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}

	if _, err := pool.Exec(ctx, `DELETE FROM a WHERE id = 1`); err != nil {
		t.Fatalf("cascade delete: %v", err)
	}
	for _, table := range []string{"b", "c"} {
		var n int32
		err := pool.QueryRow(ctx, `SELECT id FROM `+table+` LIMIT 1`).Scan(&n)
		if err == nil {
			t.Errorf("table %s: expected empty, found row id=%d", table, n)
		}
	}
}

// TestFK_OnDeleteSetNull_OnlyAffectsMatching confirms the SET NULL
// path only touches rows that actually pointed at the deleted parent.
func TestFK_OnDeleteSetNull_OnlyAffectsMatching(t *testing.T) {
	pool, ctx, cleanup := cascadeSetup(t, `REFERENCES customers(id) ON DELETE SET NULL`)
	defer cleanup()

	if _, err := pool.Exec(ctx, `DELETE FROM customers WHERE id = 2`); err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	got := orderCustomerIDs(t, pool, ctx)
	// Order 12 (customer 2) gets nulled; orders 10 and 11 (customer 1) untouched.
	if len(got) != 3 {
		t.Fatalf("got %v rows, want 3", got)
	}
	sort.Slice(got, func(i, j int) bool {
		if got[i] == nil {
			return false // nils last
		}
		if got[j] == nil {
			return true
		}
		return got[i].(int32) < got[j].(int32)
	})
	if got[0] != int32(1) || got[1] != int32(1) || got[2] != nil {
		t.Errorf("got %v, want [1 1 nil]", got)
	}
}
