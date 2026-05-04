package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func limitAllPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO t (id) VALUES (1), (2), (3), (4), (5)`,
	); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestLimitAll(t *testing.T) {
	pool, ctx, cleanup := limitAllPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `SELECT id FROM t ORDER BY id LIMIT ALL`)
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
	if len(ids) != 5 {
		t.Errorf("got %d rows, want 5", len(ids))
	}
}

func TestOffsetWithRowsNoiseWord(t *testing.T) {
	pool, ctx, cleanup := limitAllPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `SELECT id FROM t ORDER BY id OFFSET 2 ROWS`)
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
	want := []int32{3, 4, 5}
	if len(ids) != len(want) {
		t.Fatalf("got %v, want %v", ids, want)
	}
}

func TestOffsetWithRowSingularNoiseWord(t *testing.T) {
	pool, ctx, cleanup := limitAllPool(t)
	defer cleanup()
	var id int32
	if err := pool.QueryRow(ctx,
		`SELECT id FROM t ORDER BY id OFFSET 1 ROW LIMIT 1`,
	).Scan(&id); err != nil {
		t.Fatalf("query: %v", err)
	}
	if id != 2 {
		t.Errorf("got %d, want 2", id)
	}
}
