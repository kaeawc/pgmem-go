package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kaeawc/pgmem-go/postgres"
)

// TestSerialization_ConcurrentWritesOneFails: two pinned conns each
// BEGIN, INSERT into the same table, then COMMIT. The second COMMIT
// must fail with SQLSTATE 40001.
func TestSerialization_ConcurrentWritesOneFails(t *testing.T) {
	srv, err := postgres.Start(t)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	connA, err := pgx.Connect(ctx, srv.DSN())
	if err != nil {
		t.Fatalf("Connect A: %v", err)
	}
	defer connA.Close(context.Background())
	connB, err := pgx.Connect(ctx, srv.DSN())
	if err != nil {
		t.Fatalf("Connect B: %v", err)
	}
	defer connB.Close(context.Background())

	if _, err := connA.Exec(ctx, `CREATE TABLE items (id int PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}

	for _, sql := range []string{`BEGIN`} {
		if _, err := connA.Exec(ctx, sql); err != nil {
			t.Fatalf("A %q: %v", sql, err)
		}
		if _, err := connB.Exec(ctx, sql); err != nil {
			t.Fatalf("B %q: %v", sql, err)
		}
	}

	if _, err := connA.Exec(ctx, `INSERT INTO items (id) VALUES (1)`); err != nil {
		t.Fatalf("A INSERT: %v", err)
	}
	if _, err := connB.Exec(ctx, `INSERT INTO items (id) VALUES (2)`); err != nil {
		t.Fatalf("B INSERT: %v", err)
	}

	if _, err := connA.Exec(ctx, `COMMIT`); err != nil {
		t.Fatalf("A COMMIT: %v", err)
	}
	_, err = connB.Exec(ctx, `COMMIT`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "40001" {
		t.Errorf("B COMMIT: got %v, want 40001", err)
	}

	// Final state has only A's row.
	rows, err := connA.Query(ctx, `SELECT id FROM items`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	var ids []int32
	for rows.Next() {
		var n int32
		_ = rows.Scan(&n)
		ids = append(ids, n)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Errorf("final ids: got %v, want [1]", ids)
	}
}

// TestSerialization_DisjointWritesNoConflict: two conns writing to
// different tables both commit.
func TestSerialization_DisjointWritesNoConflict(t *testing.T) {
	srv, err := postgres.Start(t)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connA, _ := pgx.Connect(ctx, srv.DSN())
	defer connA.Close(context.Background())
	connB, _ := pgx.Connect(ctx, srv.DSN())
	defer connB.Close(context.Background())

	if _, err := connA.Exec(ctx, `CREATE TABLE a (n int)`); err != nil {
		t.Fatalf("CREATE a: %v", err)
	}
	if _, err := connA.Exec(ctx, `CREATE TABLE b (n int)`); err != nil {
		t.Fatalf("CREATE b: %v", err)
	}

	for _, c := range []*pgx.Conn{connA, connB} {
		if _, err := c.Exec(ctx, `BEGIN`); err != nil {
			t.Fatalf("BEGIN: %v", err)
		}
	}
	if _, err := connA.Exec(ctx, `INSERT INTO a (n) VALUES (1)`); err != nil {
		t.Fatalf("A INSERT: %v", err)
	}
	if _, err := connB.Exec(ctx, `INSERT INTO b (n) VALUES (2)`); err != nil {
		t.Fatalf("B INSERT: %v", err)
	}
	if _, err := connA.Exec(ctx, `COMMIT`); err != nil {
		t.Errorf("A COMMIT: %v", err)
	}
	if _, err := connB.Exec(ctx, `COMMIT`); err != nil {
		t.Errorf("B COMMIT: %v (disjoint tables shouldn't conflict)", err)
	}
}
