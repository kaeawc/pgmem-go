package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func alterTablePool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, name) VALUES (1, 'a'), (2, 'b')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestAlterTable_AddColumnNullable(t *testing.T) {
	pool, ctx, cleanup := alterTablePool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `ALTER TABLE t ADD COLUMN note text`); err != nil {
		t.Fatalf("ALTER ADD: %v", err)
	}
	rows, err := pool.Query(ctx, `SELECT id, name, note FROM t ORDER BY id`)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	defer rows.Close()
	var got []struct {
		id   int32
		name string
		note *string
	}
	for rows.Next() {
		var r struct {
			id   int32
			name string
			note *string
		}
		if err := rows.Scan(&r.id, &r.name, &r.note); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	for _, r := range got {
		if r.note != nil {
			t.Errorf("row %d: note = %q, want NULL", r.id, *r.note)
		}
	}

	// New column is writable.
	if _, err := pool.Exec(ctx, `UPDATE t SET note = 'hi' WHERE id = 1`); err != nil {
		t.Fatalf("UPDATE: %v", err)
	}
	var note *string
	if err := pool.QueryRow(ctx, `SELECT note FROM t WHERE id = 1`).Scan(&note); err != nil {
		t.Fatalf("select after update: %v", err)
	}
	if note == nil || *note != "hi" {
		t.Errorf("note = %v, want hi", note)
	}
}

func TestAlterTable_AddColumnNotNullOnEmpty(t *testing.T) {
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
	if _, err := pool.Exec(ctx, `CREATE TABLE empty_t (id int PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	// NOT NULL column on an empty table should succeed.
	if _, err := pool.Exec(ctx, `ALTER TABLE empty_t ADD COLUMN flag bool NOT NULL`); err != nil {
		t.Fatalf("ALTER ADD NOT NULL: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO empty_t (id, flag) VALUES (1, true)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
}

func TestAlterTable_AddColumnNotNullOnNonEmpty(t *testing.T) {
	pool, ctx, cleanup := alterTablePool(t)
	defer cleanup()
	_, err := pool.Exec(ctx, `ALTER TABLE t ADD COLUMN flag bool NOT NULL`)
	if err == nil {
		t.Fatalf("expected error adding NOT NULL column to non-empty table")
	}
}

func TestAlterTable_AddColumnDuplicate(t *testing.T) {
	pool, ctx, cleanup := alterTablePool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `ALTER TABLE t ADD COLUMN name text`); err == nil {
		t.Fatalf("expected duplicate column error")
	}
}

func TestAlterTable_AddColumnIfNotExists(t *testing.T) {
	pool, ctx, cleanup := alterTablePool(t)
	defer cleanup()
	// Parser should accept IF NOT EXISTS even though we rely on
	// catalog uniqueness for the actual collision check.
	if _, err := pool.Exec(ctx, `ALTER TABLE t ADD COLUMN IF NOT EXISTS extra int`); err != nil {
		t.Fatalf("ALTER ADD IF NOT EXISTS: %v", err)
	}
}

func TestAlterTable_DropColumn(t *testing.T) {
	pool, ctx, cleanup := alterTablePool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `ALTER TABLE t DROP COLUMN name`); err != nil {
		t.Fatalf("ALTER DROP: %v", err)
	}
	// name is gone — referencing it now errors.
	if _, err := pool.Query(ctx, `SELECT name FROM t`); err == nil {
		t.Fatalf("expected error referencing dropped column")
	}
	// id is still there.
	var got int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&got); err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != 2 {
		t.Errorf("count = %d, want 2", got)
	}
	var id int32
	if err := pool.QueryRow(ctx, `SELECT id FROM t WHERE id = 1`).Scan(&id); err != nil {
		t.Fatalf("select id: %v", err)
	}
	if id != 1 {
		t.Errorf("id = %d, want 1", id)
	}
}

func TestAlterTable_DropColumnIfExistsMissing(t *testing.T) {
	pool, ctx, cleanup := alterTablePool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `ALTER TABLE t DROP COLUMN IF EXISTS missing`); err != nil {
		t.Errorf("DROP IF EXISTS missing: %v", err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE t DROP COLUMN missing`); err == nil {
		t.Errorf("expected error dropping nonexistent column")
	}
}

func TestAlterTable_DropColumnCascade(t *testing.T) {
	pool, ctx, cleanup := alterTablePool(t)
	defer cleanup()
	// CASCADE/RESTRICT are accepted and ignored.
	if _, err := pool.Exec(ctx, `ALTER TABLE t DROP COLUMN name CASCADE`); err != nil {
		t.Fatalf("DROP CASCADE: %v", err)
	}
}

func TestAlterTable_RenameColumn(t *testing.T) {
	pool, ctx, cleanup := alterTablePool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `ALTER TABLE t RENAME COLUMN name TO label`); err != nil {
		t.Fatalf("ALTER RENAME: %v", err)
	}
	var label string
	if err := pool.QueryRow(ctx, `SELECT label FROM t WHERE id = 1`).Scan(&label); err != nil {
		t.Fatalf("select label: %v", err)
	}
	if label != "a" {
		t.Errorf("label = %q, want a", label)
	}
	if _, err := pool.Query(ctx, `SELECT name FROM t`); err == nil {
		t.Fatalf("expected error referencing old name")
	}
}

func TestAlterTable_RenameColumnConflict(t *testing.T) {
	pool, ctx, cleanup := alterTablePool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `ALTER TABLE t RENAME COLUMN name TO id`); err == nil {
		t.Fatalf("expected conflict renaming to existing column")
	}
}

func TestAlterTable_MissingTable(t *testing.T) {
	pool, ctx, cleanup := alterTablePool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `ALTER TABLE nope ADD COLUMN x int`); err == nil {
		t.Fatalf("expected error for missing table")
	}
}
