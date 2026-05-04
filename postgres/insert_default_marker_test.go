package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func defaultMarkerPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

func TestInsertDefault_PicksUpColumnDefault(t *testing.T) {
	pool, ctx, cleanup := defaultMarkerPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, status text NOT NULL DEFAULT 'pending')`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, status) VALUES (1, DEFAULT)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	var got string
	if err := pool.QueryRow(ctx, `SELECT status FROM t WHERE id = 1`).Scan(&got); err != nil {
		t.Fatalf("select: %v", err)
	}
	if got != "pending" {
		t.Errorf("status = %q, want pending", got)
	}
}

func TestInsertDefault_NoColumnDefaultLeavesNull(t *testing.T) {
	pool, ctx, cleanup := defaultMarkerPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, note text)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	// note has no DEFAULT — explicit DEFAULT keyword should produce
	// NULL, matching real PG.
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, note) VALUES (1, DEFAULT)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	var note *string
	if err := pool.QueryRow(ctx, `SELECT note FROM t WHERE id = 1`).Scan(&note); err != nil {
		t.Fatalf("select: %v", err)
	}
	if note != nil {
		t.Errorf("note = %v, want NULL", *note)
	}
}

func TestInsertDefault_AutoColumn(t *testing.T) {
	pool, ctx, cleanup := defaultMarkerPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id serial PRIMARY KEY, label text NOT NULL DEFAULT 'x')`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	// Auto column with explicit DEFAULT should still get the next
	// sequence value, not be left NULL.
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, label) VALUES (DEFAULT, DEFAULT)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	var id int32
	var label string
	if err := pool.QueryRow(ctx, `SELECT id, label FROM t`).Scan(&id, &label); err != nil {
		t.Fatalf("select: %v", err)
	}
	if id < 1 {
		t.Errorf("id = %d, want >=1", id)
	}
	if label != "x" {
		t.Errorf("label = %q, want x", label)
	}
}

func TestInsertDefault_MultiRow(t *testing.T) {
	pool, ctx, cleanup := defaultMarkerPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, status text DEFAULT 'pending')`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, status) VALUES (1, 'live'), (2, DEFAULT), (3, 'archived')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	rows, err := pool.Query(ctx, `SELECT id, status FROM t ORDER BY id`)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	defer rows.Close()
	want := map[int32]string{1: "live", 2: "pending", 3: "archived"}
	for rows.Next() {
		var id int32
		var st string
		if err := rows.Scan(&id, &st); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if want[id] != st {
			t.Errorf("id=%d: got %q, want %q", id, st, want[id])
		}
	}
}
