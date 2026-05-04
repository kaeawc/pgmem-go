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

// fkSetup creates parent + child tables with one parent row.
func fkSetup(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	if _, err := pool.Exec(ctx, `CREATE TABLE orders (id int PRIMARY KEY, customer_id int NOT NULL REFERENCES customers(id))`); err != nil {
		t.Fatalf("CREATE orders: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO customers (id, name) VALUES (1, 'alice')`); err != nil {
		t.Fatalf("INSERT customer: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

// TestFK_Insert_RejectsMissingParent: inserting a child row whose FK
// doesn't exist in the parent surfaces SQLSTATE 23503.
func TestFK_Insert_RejectsMissingParent(t *testing.T) {
	pool, ctx, cleanup := fkSetup(t)
	defer cleanup()

	_, err := pool.Exec(ctx, `INSERT INTO orders (id, customer_id) VALUES (10, 999)`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Errorf("got %v, want 23503", err)
	}
}

// TestFK_Insert_AcceptsExistingParent: matching value is fine.
func TestFK_Insert_AcceptsExistingParent(t *testing.T) {
	pool, ctx, cleanup := fkSetup(t)
	defer cleanup()

	if _, err := pool.Exec(ctx, `INSERT INTO orders (id, customer_id) VALUES (10, 1)`); err != nil {
		t.Errorf("got %v, want success", err)
	}
}

// TestFK_Update_RejectsMissingParent: updating a FK column to a value
// that doesn't exist in the parent rejects.
func TestFK_Update_RejectsMissingParent(t *testing.T) {
	pool, ctx, cleanup := fkSetup(t)
	defer cleanup()

	if _, err := pool.Exec(ctx, `INSERT INTO orders (id, customer_id) VALUES (10, 1)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := pool.Exec(ctx, `UPDATE orders SET customer_id = 42 WHERE id = 10`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Errorf("got %v, want 23503", err)
	}
}

// TestFK_Delete_ParentWithChildren_Restricts: deleting the parent
// while children exist surfaces 23503 (RESTRICT default).
func TestFK_Delete_ParentWithChildren_Restricts(t *testing.T) {
	pool, ctx, cleanup := fkSetup(t)
	defer cleanup()

	if _, err := pool.Exec(ctx, `INSERT INTO orders (id, customer_id) VALUES (10, 1)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := pool.Exec(ctx, `DELETE FROM customers WHERE id = 1`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Errorf("got %v, want 23503", err)
	}
	// Verify the parent is still there.
	var n int32
	if err := pool.QueryRow(ctx, `SELECT id FROM customers WHERE id = 1`).Scan(&n); err != nil {
		t.Errorf("readback: %v", err)
	}
}

// TestFK_Delete_ParentWithoutChildren_OK: a parent with no current
// child references is deletable.
func TestFK_Delete_ParentWithoutChildren_OK(t *testing.T) {
	pool, ctx, cleanup := fkSetup(t)
	defer cleanup()

	if _, err := pool.Exec(ctx, `DELETE FROM customers WHERE id = 1`); err != nil {
		t.Errorf("got %v, want success", err)
	}
}

// TestFK_NullValueAllowed: NULL on a nullable FK column bypasses the
// existence check (SQL "match simple" semantics — matches PG default).
func TestFK_NullValueAllowed(t *testing.T) {
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
	if _, err := pool.Exec(ctx, `CREATE TABLE customers (id int PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE notes (id int PRIMARY KEY, customer_id int REFERENCES customers(id))`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	// customer_id is nullable here — nullable FK with NULL value is OK.
	if _, err := pool.Exec(ctx, `INSERT INTO notes (id, customer_id) VALUES ($1, $2)`, int32(1), nil); err != nil {
		t.Errorf("got %v, want success", err)
	}
}

// TestFK_VisibleWithinTransaction: an INSERT into the parent within a
// txn is visible to a follow-up INSERT into the child in the same txn.
// Confirms FK lookup goes through Txn, not directly to canonical state.
func TestFK_VisibleWithinTransaction(t *testing.T) {
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
	if _, err := pool.Exec(ctx, `CREATE TABLE customers (id int PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE notes (id int PRIMARY KEY, customer_id int NOT NULL REFERENCES customers(id))`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `BEGIN`); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO customers (id) VALUES (1)`); err != nil {
		t.Fatalf("INSERT customer: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO notes (id, customer_id) VALUES (10, 1)`); err != nil {
		t.Errorf("child INSERT in same tx: got %v, want success", err)
	}
	if _, err := conn.Exec(ctx, `COMMIT`); err != nil {
		t.Fatalf("COMMIT: %v", err)
	}
}
