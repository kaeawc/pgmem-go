package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func defaultValuesPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

// TestInsertDefaultValues_AutoFilled: a table with only an auto-fill
// (BIGSERIAL) column accepts DEFAULT VALUES and produces id=1.
func TestInsertDefaultValues_AutoFilled(t *testing.T) {
	pool, ctx, cleanup := defaultValuesPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE seq (id bigserial PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO seq DEFAULT VALUES`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO seq DEFAULT VALUES`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	var got int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM seq`).Scan(&got); err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != 2 {
		t.Errorf("got %d rows, want 2", got)
	}
}

// TestInsertDefaultValues_Returning: RETURNING * works with DEFAULT
// VALUES too.
func TestInsertDefaultValues_Returning(t *testing.T) {
	pool, ctx, cleanup := defaultValuesPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE seq (id bigserial PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	var id int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO seq DEFAULT VALUES RETURNING *`,
	).Scan(&id); err != nil {
		t.Fatalf("query: %v", err)
	}
	if id < 1 {
		t.Errorf("got id %d, want >= 1", id)
	}
}

// TestInsertDefaultValues_NotNullStillEnforced: a NOT NULL column
// without an auto-fill rejects DEFAULT VALUES with 23502.
func TestInsertDefaultValues_NotNullStillEnforced(t *testing.T) {
	pool, ctx, cleanup := defaultValuesPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx,
		`CREATE TABLE t (id bigserial PRIMARY KEY, name text NOT NULL)`,
	); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	_, err := pool.Exec(ctx, `INSERT INTO t DEFAULT VALUES`)
	if err == nil {
		t.Fatal("expected NOT NULL violation, got nil")
	}
}
