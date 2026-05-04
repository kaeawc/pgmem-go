package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func indexNoopPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

func TestCreateIndex_Plain(t *testing.T) {
	pool, ctx, cleanup := indexNoopPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE INDEX t_name_idx ON t (name)`); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	// Reads still work.
	var got int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&got); err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != 2 {
		t.Errorf("got %d, want 2", got)
	}
}

func TestCreateIndex_AllOptions(t *testing.T) {
	pool, ctx, cleanup := indexNoopPool(t)
	defer cleanup()
	stmts := []string{
		`CREATE UNIQUE INDEX t_uniq ON t (id)`,
		`CREATE INDEX CONCURRENTLY t_conc ON t USING btree (name)`,
		`CREATE INDEX IF NOT EXISTS t_partial ON t (id) WHERE id > 0`,
		`CREATE INDEX ON t (name, id)`, // anonymous index name
	}
	for _, s := range stmts {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
}

func TestDropIndex(t *testing.T) {
	pool, ctx, cleanup := indexNoopPool(t)
	defer cleanup()
	// We don't actually maintain an index registry, so DROP INDEX is
	// always a no-op success regardless of whether the index "existed."
	if _, err := pool.Exec(ctx, `CREATE INDEX t_name_idx ON t (name)`); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	if _, err := pool.Exec(ctx, `DROP INDEX t_name_idx`); err != nil {
		t.Fatalf("DROP INDEX: %v", err)
	}
	if _, err := pool.Exec(ctx, `DROP INDEX IF EXISTS missing`); err != nil {
		t.Errorf("DROP INDEX IF EXISTS: %v", err)
	}
	if _, err := pool.Exec(ctx, `DROP INDEX CONCURRENTLY IF EXISTS missing`); err != nil {
		t.Errorf("DROP INDEX CONCURRENTLY IF EXISTS: %v", err)
	}
}
