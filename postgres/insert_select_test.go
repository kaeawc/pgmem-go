package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func insertSelectPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
		`CREATE TABLE src (id int PRIMARY KEY, name text NOT NULL)`,
		`CREATE TABLE dst (id int PRIMARY KEY, name text NOT NULL)`,
		`INSERT INTO src (id, name) VALUES (1, 'a'), (2, 'b'), (3, 'c')`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestInsertSelect_Basic(t *testing.T) {
	pool, ctx, cleanup := insertSelectPool(t)
	defer cleanup()
	tag, err := pool.Exec(ctx, `INSERT INTO dst (id, name) SELECT id, name FROM src`)
	if err != nil {
		t.Fatalf("insert select: %v", err)
	}
	if tag.RowsAffected() != 3 {
		t.Errorf("RowsAffected = %d, want 3", tag.RowsAffected())
	}
	var count int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dst`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
}

func TestInsertSelect_WithWhere(t *testing.T) {
	pool, ctx, cleanup := insertSelectPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx,
		`INSERT INTO dst (id, name) SELECT id, name FROM src WHERE id > 1`,
	); err != nil {
		t.Fatalf("insert select: %v", err)
	}
	var count int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dst`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}

func TestInsertSelect_Returning(t *testing.T) {
	pool, ctx, cleanup := insertSelectPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx,
		`INSERT INTO dst (id, name) SELECT id, name FROM src ORDER BY id RETURNING id`)
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
	if len(ids) != 3 {
		t.Errorf("got %d returning rows, want 3", len(ids))
	}
}

func TestInsertSelect_ColumnMismatchErrors(t *testing.T) {
	pool, ctx, cleanup := insertSelectPool(t)
	defer cleanup()
	_, err := pool.Exec(ctx, `INSERT INTO dst (id) SELECT id, name FROM src`)
	if err == nil {
		t.Fatal("expected column-count mismatch error")
	}
}
