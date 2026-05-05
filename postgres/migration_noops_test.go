package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func migNoopPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

func TestMigrationNoop_CreateExtension(t *testing.T) {
	pool, ctx, cleanup := migNoopPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`); err != nil {
		t.Fatalf("CREATE EXTENSION: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE EXTENSION pgcrypto WITH SCHEMA public VERSION '1.3'`); err != nil {
		t.Fatalf("CREATE EXTENSION WITH SCHEMA: %v", err)
	}
}

func TestMigrationNoop_DropExtension(t *testing.T) {
	pool, ctx, cleanup := migNoopPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `DROP EXTENSION IF EXISTS "uuid-ossp" CASCADE`); err != nil {
		t.Fatalf("DROP EXTENSION: %v", err)
	}
}

func TestMigrationNoop_CommentOn(t *testing.T) {
	pool, ctx, cleanup := migNoopPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	cases := []string{
		`COMMENT ON TABLE t IS 'a table'`,
		`COMMENT ON COLUMN t.id IS 'primary key'`,
		`COMMENT ON SCHEMA public IS 'default'`,
		`COMMENT ON EXTENSION "uuid-ossp" IS 'uuids'`,
	}
	for _, sql := range cases {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Errorf("%q: %v", sql, err)
		}
	}
}

func TestMigrationNoop_Reset(t *testing.T) {
	pool, ctx, cleanup := migNoopPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `RESET ALL`); err != nil {
		t.Fatalf("RESET ALL: %v", err)
	}
	if _, err := pool.Exec(ctx, `RESET search_path`); err != nil {
		t.Fatalf("RESET search_path: %v", err)
	}
}

func TestMigrationNoop_DiscardAll(t *testing.T) {
	pool, ctx, cleanup := migNoopPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `DISCARD ALL`); err != nil {
		t.Fatalf("DISCARD ALL: %v", err)
	}
}

func TestMigrationNoop_AnalyzeVacuum(t *testing.T) {
	pool, ctx, cleanup := migNoopPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `ANALYZE t`); err != nil {
		t.Errorf("ANALYZE: %v", err)
	}
	if _, err := pool.Exec(ctx, `VACUUM ANALYZE t`); err != nil {
		t.Errorf("VACUUM ANALYZE: %v", err)
	}
	if _, err := pool.Exec(ctx, `VACUUM`); err != nil {
		t.Errorf("VACUUM: %v", err)
	}
}

func TestMigrationNoop_CreateDropSchema(t *testing.T) {
	pool, ctx, cleanup := migNoopPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS public`); err != nil {
		t.Errorf("CREATE SCHEMA: %v", err)
	}
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS old CASCADE`); err != nil {
		t.Errorf("DROP SCHEMA: %v", err)
	}
}
