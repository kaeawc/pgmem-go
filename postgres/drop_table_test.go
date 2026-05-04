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

func dropPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

// TestDropTable_RemovesTable: after DROP, subsequent SELECT errors
// because the table no longer exists.
func TestDropTable_RemovesTable(t *testing.T) {
	pool, ctx, cleanup := dropPool(t)
	defer cleanup()

	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `DROP TABLE t`); err != nil {
		t.Fatalf("DROP: %v", err)
	}
	_, err := pool.Exec(ctx, `SELECT id FROM t`)
	if err == nil {
		t.Fatal("SELECT after DROP: want error, got nil")
	}
}

// TestDropTable_MissingErrors: DROP TABLE on a nonexistent table is
// SQLSTATE 42P01 ("undefined_table").
func TestDropTable_MissingErrors(t *testing.T) {
	pool, ctx, cleanup := dropPool(t)
	defer cleanup()
	_, err := pool.Exec(ctx, `DROP TABLE nope`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42P01" {
		t.Errorf("got %v, want 42P01", err)
	}
}

// TestDropTable_IfExists is a no-op on a missing table.
func TestDropTable_IfExists(t *testing.T) {
	pool, ctx, cleanup := dropPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS nope`); err != nil {
		t.Errorf("got %v, want success", err)
	}
}

// TestDropTable_RecreateWorks: dropping then recreating a table with
// the same name succeeds and starts fresh (no leftover rows).
func TestDropTable_RecreateWorks(t *testing.T) {
	pool, ctx, cleanup := dropPool(t)
	defer cleanup()

	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (id) VALUES (1)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if _, err := pool.Exec(ctx, `DROP TABLE t`); err != nil {
		t.Fatalf("DROP: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY)`); err != nil {
		t.Fatalf("re-CREATE: %v", err)
	}
	rows, err := pool.Query(ctx, `SELECT id FROM t`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	for rows.Next() {
		t.Errorf("recreated table should be empty")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
}
