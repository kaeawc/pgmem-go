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

func subquerySetup(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	if _, err := pool.Exec(ctx, `CREATE TABLE customers (id int PRIMARY KEY, name text NOT NULL)`); err != nil {
		t.Fatalf("CREATE customers: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE orders (id int PRIMARY KEY, customer_id int NOT NULL)`); err != nil {
		t.Fatalf("CREATE orders: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO customers (id, name) VALUES (1, 'alice'), (2, 'bob'), (3, 'carol')`); err != nil {
		t.Fatalf("INSERT customers: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO orders (id, customer_id) VALUES (10, 1), (11, 2)`); err != nil {
		t.Fatalf("INSERT orders: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

// TestScalarSubquery_OverWire: a `(SELECT ...)` used as a value
// expression returns a single value. Here we count rows and use the
// count in a comparison.
func TestScalarSubquery_OverWire(t *testing.T) {
	pool, ctx, cleanup := subquerySetup(t)
	defer cleanup()

	var n int32
	if err := pool.QueryRow(ctx, `SELECT (SELECT id FROM customers WHERE name = 'alice')`).Scan(&n); err != nil {
		t.Fatalf("SELECT scalar: %v", err)
	}
	if n != 1 {
		t.Errorf("scalar id: got %d, want 1", n)
	}
}

// TestScalarSubquery_EmptyReturnsNull: an empty subquery returns NULL,
// not an error.
func TestScalarSubquery_EmptyReturnsNull(t *testing.T) {
	pool, ctx, cleanup := subquerySetup(t)
	defer cleanup()

	var n *int32
	if err := pool.QueryRow(ctx, `SELECT (SELECT id FROM customers WHERE name = 'no-one')`).Scan(&n); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if n != nil {
		t.Errorf("empty scalar: got %v, want nil", n)
	}
}

// TestScalarSubquery_MultipleRowsErrors covers the SQLSTATE 21000 case.
func TestScalarSubquery_MultipleRowsErrors(t *testing.T) {
	pool, ctx, cleanup := subquerySetup(t)
	defer cleanup()

	_, err := pool.Exec(ctx, `SELECT (SELECT id FROM customers)`)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "21000" {
		t.Errorf("got %v, want 21000", err)
	}
}

// TestInList_OverWire: `WHERE id IN (1, 3)` returns matching rows.
func TestInList_OverWire(t *testing.T) {
	pool, ctx, cleanup := subquerySetup(t)
	defer cleanup()

	rows, err := pool.Query(ctx, `SELECT id FROM customers WHERE id IN (1, 3)`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	var got []int32
	for rows.Next() {
		var n int32
		_ = rows.Scan(&n)
		got = append(got, n)
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	want := []int32{1, 3}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestInSubquery_OverWire: customers WHERE id IN (SELECT customer_id
// FROM orders) — i.e. customers with at least one order.
func TestInSubquery_OverWire(t *testing.T) {
	pool, ctx, cleanup := subquerySetup(t)
	defer cleanup()

	rows, err := pool.Query(ctx,
		`SELECT name FROM customers WHERE id IN (SELECT customer_id FROM orders) ORDER BY name`,
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	var got []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		got = append(got, s)
	}
	if len(got) != 2 || got[0] != "alice" || got[1] != "bob" {
		t.Errorf("got %v, want [alice bob]", got)
	}
}

// TestNotInSubquery_OverWire: NOT IN inverts. Carol has no orders.
func TestNotInSubquery_OverWire(t *testing.T) {
	pool, ctx, cleanup := subquerySetup(t)
	defer cleanup()

	rows, err := pool.Query(ctx,
		`SELECT name FROM customers WHERE id NOT IN (SELECT customer_id FROM orders)`,
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	var got []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		got = append(got, s)
	}
	if len(got) != 1 || got[0] != "carol" {
		t.Errorf("got %v, want [carol]", got)
	}
}

// TestInSubquery_WithParameter: parameter on the IN-list side, which
// pgx uses for SELECT ... FROM t WHERE col = ANY($1) shape rewrites.
// We support direct $N inside IN literally.
func TestInSubquery_WithParameter(t *testing.T) {
	pool, ctx, cleanup := subquerySetup(t)
	defer cleanup()

	rows, err := pool.Query(ctx,
		`SELECT id FROM customers WHERE name IN ($1, $2) ORDER BY id`,
		"alice", "carol",
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	var got []int32
	for rows.Next() {
		var n int32
		_ = rows.Scan(&n)
		got = append(got, n)
	}
	if len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Errorf("got %v, want [1 3]", got)
	}
}
