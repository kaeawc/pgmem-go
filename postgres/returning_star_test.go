package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func returningPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, name text NOT NULL, n int)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestReturningStar_Insert(t *testing.T) {
	pool, ctx, cleanup := returningPool(t)
	defer cleanup()
	var id int32
	var name string
	var n *int32
	if err := pool.QueryRow(ctx,
		`INSERT INTO t (id, name, n) VALUES (1, 'alice', 10) RETURNING *`,
	).Scan(&id, &name, &n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if id != 1 || name != "alice" || n == nil || *n != 10 {
		t.Errorf("got id=%d name=%q n=%v, want 1/alice/10", id, name, n)
	}
}

func TestReturningStar_Update(t *testing.T) {
	pool, ctx, cleanup := returningPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, name, n) VALUES (2, 'bob', 5)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	var id int32
	var name string
	var n *int32
	if err := pool.QueryRow(ctx,
		`UPDATE t SET n = n + 1 WHERE id = 2 RETURNING *`,
	).Scan(&id, &name, &n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if id != 2 || name != "bob" || n == nil || *n != 6 {
		t.Errorf("got id=%d name=%q n=%v, want 2/bob/6", id, name, n)
	}
}

func TestReturningStar_Delete(t *testing.T) {
	pool, ctx, cleanup := returningPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, name, n) VALUES (3, 'carol', NULL)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	var id int32
	var name string
	var n *int32
	if err := pool.QueryRow(ctx,
		`DELETE FROM t WHERE id = 3 RETURNING *`,
	).Scan(&id, &name, &n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if id != 3 || name != "carol" || n != nil {
		t.Errorf("got id=%d name=%q n=%v, want 3/carol/NULL", id, name, n)
	}
}

func TestReturningStar_MixedWithExpr(t *testing.T) {
	pool, ctx, cleanup := returningPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx,
		`INSERT INTO t (id, name, n) VALUES (4, 'dan', 100) RETURNING *, n * 2`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	desc := rows.FieldDescriptions()
	if len(desc) != 4 {
		t.Errorf("cols: got %d, want 4 (id, name, n, n*2)", len(desc))
	}
}
