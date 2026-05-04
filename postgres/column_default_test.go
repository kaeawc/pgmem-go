package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func defaultPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

func TestColumnDefault_LiteralFillsOmittedColumn(t *testing.T) {
	pool, ctx, cleanup := defaultPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, status text NOT NULL DEFAULT 'pending')`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (id) VALUES (1)`); err != nil {
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

func TestColumnDefault_ExplicitValueWins(t *testing.T) {
	pool, ctx, cleanup := defaultPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, status text NOT NULL DEFAULT 'pending')`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, status) VALUES (1, 'active')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	var got string
	if err := pool.QueryRow(ctx, `SELECT status FROM t WHERE id = 1`).Scan(&got); err != nil {
		t.Fatalf("select: %v", err)
	}
	if got != "active" {
		t.Errorf("status = %q, want active", got)
	}
}

func TestColumnDefault_ExplicitNullKeepsNull(t *testing.T) {
	pool, ctx, cleanup := defaultPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, note text DEFAULT 'hi')`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	// User explicitly inserts NULL — default does NOT kick in. Real
	// PG behaves the same way: DEFAULT only applies when the column
	// isn't mentioned in the column list.
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, note) VALUES (1, NULL)`); err != nil {
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

func TestColumnDefault_NowFillsTimestamp(t *testing.T) {
	pool, ctx, cleanup := defaultPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, created_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	before := time.Now().Add(-time.Second)
	if _, err := pool.Exec(ctx, `INSERT INTO t (id) VALUES (1)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	after := time.Now().Add(time.Second)
	var got time.Time
	if err := pool.QueryRow(ctx, `SELECT created_at FROM t WHERE id = 1`).Scan(&got); err != nil {
		t.Fatalf("select: %v", err)
	}
	if got.Before(before) || got.After(after) {
		t.Errorf("created_at = %v, want between %v and %v", got, before, after)
	}
}

func TestColumnDefault_DefaultValuesUsesDefaults(t *testing.T) {
	pool, ctx, cleanup := defaultPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id serial PRIMARY KEY, status text NOT NULL DEFAULT 'queued')`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t DEFAULT VALUES`); err != nil {
		t.Fatalf("INSERT DEFAULT VALUES: %v", err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM t`).Scan(&status); err != nil {
		t.Fatalf("select: %v", err)
	}
	if status != "queued" {
		t.Errorf("status = %q, want queued", status)
	}
}

func TestColumnDefault_AlterAddColumnWithDefault(t *testing.T) {
	pool, ctx, cleanup := defaultPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (id) VALUES (1)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	// ALTER ADD with DEFAULT — pgmem-go does NOT backfill existing
	// rows with the default (real PG does, but we leave them NULL
	// for now). New inserts that omit the column do get the default.
	if _, err := pool.Exec(ctx, `ALTER TABLE t ADD COLUMN status text DEFAULT 'new'`); err != nil {
		t.Fatalf("ALTER ADD: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (id) VALUES (2)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	var status *string
	if err := pool.QueryRow(ctx, `SELECT status FROM t WHERE id = 2`).Scan(&status); err != nil {
		t.Fatalf("select: %v", err)
	}
	if status == nil || *status != "new" {
		t.Errorf("row 2 status = %v, want new", status)
	}
}
