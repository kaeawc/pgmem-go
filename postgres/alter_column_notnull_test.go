package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func alterColPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

func TestAlterColumn_DropNotNull(t *testing.T) {
	pool, ctx, cleanup := alterColPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, name text NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, name) VALUES (1, 'a')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	// Inserting NULL into a NOT NULL column should fail.
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, name) VALUES (2, NULL)`); err == nil {
		t.Fatalf("expected NOT NULL violation before ALTER")
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE t ALTER COLUMN name DROP NOT NULL`); err != nil {
		t.Fatalf("ALTER COLUMN DROP NOT NULL: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, name) VALUES (2, NULL)`); err != nil {
		t.Fatalf("INSERT after DROP NOT NULL: %v", err)
	}
}

func TestAlterColumn_SetNotNull_NoNulls(t *testing.T) {
	pool, ctx, cleanup := alterColPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, label text)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, label) VALUES (1, 'a'), (2, 'b')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE t ALTER COLUMN label SET NOT NULL`); err != nil {
		t.Fatalf("ALTER COLUMN SET NOT NULL: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, label) VALUES (3, NULL)`); err == nil {
		t.Fatalf("expected NOT NULL violation after ALTER")
	}
}

func TestAlterColumn_SetNotNull_Existing(t *testing.T) {
	pool, ctx, cleanup := alterColPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, label text)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, label) VALUES (1, NULL)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE t ALTER COLUMN label SET NOT NULL`); err == nil {
		t.Fatalf("expected error setting NOT NULL on column with existing NULLs")
	}
}

func TestAlterColumn_SetNotNull_OptionalColumnKeyword(t *testing.T) {
	pool, ctx, cleanup := alterColPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, label text)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	// COLUMN keyword is optional in PG.
	if _, err := pool.Exec(ctx, `ALTER TABLE t ALTER label SET NOT NULL`); err != nil {
		t.Fatalf("ALTER without COLUMN keyword: %v", err)
	}
}

func TestAlterColumn_MissingColumn(t *testing.T) {
	pool, ctx, cleanup := alterColPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE t ALTER COLUMN nope SET NOT NULL`); err == nil {
		t.Fatalf("expected error for missing column")
	}
}
