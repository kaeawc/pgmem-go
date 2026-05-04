package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func conflictPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, name text NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, name) VALUES (1, 'alice')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestOnConflict_DoNothing_Skips(t *testing.T) {
	pool, ctx, cleanup := conflictPool(t)
	defer cleanup()
	tag, err := pool.Exec(ctx,
		`INSERT INTO t (id, name) VALUES (1, 'duplicate') ON CONFLICT (id) DO NOTHING`)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if tag.RowsAffected() != 0 {
		t.Errorf("RowsAffected = %d, want 0", tag.RowsAffected())
	}
	// Original row still wins.
	var name string
	if err := pool.QueryRow(ctx, `SELECT name FROM t WHERE id = 1`).Scan(&name); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if name != "alice" {
		t.Errorf("name = %q, want alice", name)
	}
}

func TestOnConflict_DoNothing_InsertsNew(t *testing.T) {
	pool, ctx, cleanup := conflictPool(t)
	defer cleanup()
	tag, err := pool.Exec(ctx,
		`INSERT INTO t (id, name) VALUES (2, 'bob') ON CONFLICT (id) DO NOTHING`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Errorf("RowsAffected = %d, want 1", tag.RowsAffected())
	}
}

func TestOnConflict_DoNothing_MixedBatch(t *testing.T) {
	pool, ctx, cleanup := conflictPool(t)
	defer cleanup()
	// id=1 conflicts; id=3 is new.
	tag, err := pool.Exec(ctx, `
		INSERT INTO t (id, name) VALUES (1, 'dup'), (3, 'carol')
		ON CONFLICT (id) DO NOTHING`)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Errorf("RowsAffected = %d, want 1", tag.RowsAffected())
	}
	var count int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 { // alice, carol
		t.Errorf("count = %d, want 2", count)
	}
}

func TestOnConflict_DoUpdate_Excluded(t *testing.T) {
	pool, ctx, cleanup := conflictPool(t)
	defer cleanup()
	tag, err := pool.Exec(ctx, `
		INSERT INTO t (id, name) VALUES (1, 'updated')
		ON CONFLICT (id) DO UPDATE SET name = excluded.name`)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Errorf("RowsAffected = %d, want 1", tag.RowsAffected())
	}
	var name string
	if err := pool.QueryRow(ctx, `SELECT name FROM t WHERE id = 1`).Scan(&name); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if name != "updated" {
		t.Errorf("name = %q, want updated", name)
	}
}

func TestOnConflict_DoUpdate_ReferencesExisting(t *testing.T) {
	pool, ctx, cleanup := conflictPool(t)
	defer cleanup()
	// Append the proposed name to the existing one.
	if _, err := pool.Exec(ctx, `
		INSERT INTO t (id, name) VALUES (1, 'bob')
		ON CONFLICT (id) DO UPDATE SET name = name || ' / ' || excluded.name`); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	var name string
	if err := pool.QueryRow(ctx, `SELECT name FROM t WHERE id = 1`).Scan(&name); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if name != "alice / bob" {
		t.Errorf("name = %q, want %q", name, "alice / bob")
	}
}

func TestOnConflict_DoUpdate_Returning(t *testing.T) {
	pool, ctx, cleanup := conflictPool(t)
	defer cleanup()
	var id int32
	var name string
	if err := pool.QueryRow(ctx, `
		INSERT INTO t (id, name) VALUES (1, 'changed')
		ON CONFLICT (id) DO UPDATE SET name = excluded.name
		RETURNING id, name`).Scan(&id, &name); err != nil {
		t.Fatalf("query: %v", err)
	}
	if id != 1 || name != "changed" {
		t.Errorf("got id=%d name=%q, want 1/changed", id, name)
	}
}

func TestOnConflict_DoUpdate_NewRowStillInserts(t *testing.T) {
	pool, ctx, cleanup := conflictPool(t)
	defer cleanup()
	tag, err := pool.Exec(ctx, `
		INSERT INTO t (id, name) VALUES (5, 'new')
		ON CONFLICT (id) DO UPDATE SET name = excluded.name`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Errorf("RowsAffected = %d, want 1", tag.RowsAffected())
	}
	var count int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 { // alice + new
		t.Errorf("count = %d, want 2", count)
	}
}

func TestOnConflict_Returning_OnlyForActualInserts(t *testing.T) {
	pool, ctx, cleanup := conflictPool(t)
	defer cleanup()
	// Conflict — no row inserted, no rows returned.
	rows, err := pool.Query(ctx, `
		INSERT INTO t (id, name) VALUES (1, 'dup')
		ON CONFLICT (id) DO NOTHING RETURNING id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Errorf("got a returning row for a skipped conflict")
	}
}
