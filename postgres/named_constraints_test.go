package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func nameConstrPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

func TestNamedConstraint_ColumnLevelPK(t *testing.T) {
	pool, ctx, cleanup := nameConstrPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int CONSTRAINT t_pk PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (id) VALUES (1)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (id) VALUES (1)`); err == nil {
		t.Fatalf("expected duplicate-PK error")
	}
}

func TestNamedConstraint_ColumnLevelCheck(t *testing.T) {
	pool, ctx, cleanup := nameConstrPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, age int CONSTRAINT age_nonneg CHECK (age >= 0))`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, age) VALUES (1, 5)`); err != nil {
		t.Fatalf("valid INSERT: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, age) VALUES (2, -1)`); err == nil {
		t.Fatalf("expected check violation")
	}
}

func TestNamedConstraint_TableLevelCheck(t *testing.T) {
	pool, ctx, cleanup := nameConstrPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE t (
		  id    int PRIMARY KEY,
		  lo    int NOT NULL,
		  hi    int NOT NULL,
		  CONSTRAINT lo_le_hi CHECK (lo <= hi)
		)
	`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, lo, hi) VALUES (1, 0, 10)`); err != nil {
		t.Fatalf("valid INSERT: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, lo, hi) VALUES (2, 10, 0)`); err == nil {
		t.Fatalf("expected check violation")
	}
}

func TestNamedConstraint_TableLevelCheckNoName(t *testing.T) {
	pool, ctx, cleanup := nameConstrPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE t (
		  id  int PRIMARY KEY,
		  qty int NOT NULL,
		  CHECK (qty > 0)
		)
	`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, qty) VALUES (1, 0)`); err == nil {
		t.Fatalf("expected check violation")
	}
}

func TestNamedConstraint_TableLevelPKKeepsUnique(t *testing.T) {
	pool, ctx, cleanup := nameConstrPool(t)
	defer cleanup()
	// Composite PK isn't yet enforced as a composite uniqueness
	// constraint — but the parser must accept the syntax (CONSTRAINT
	// name + PRIMARY KEY (col1, col2)) so real schemas load.
	if _, err := pool.Exec(ctx, `
		CREATE TABLE t (
		  a int NOT NULL,
		  b int NOT NULL,
		  CONSTRAINT t_pk PRIMARY KEY (a, b)
		)
	`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (a, b) VALUES (1, 2), (1, 3)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
}

func TestNamedConstraint_NameRoundTripsInError(t *testing.T) {
	pool, ctx, cleanup := nameConstrPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE t (
		  id  int PRIMARY KEY,
		  qty int NOT NULL,
		  CONSTRAINT qty_positive CHECK (qty > 0)
		)
	`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	_, err := pool.Exec(ctx, `INSERT INTO t (id, qty) VALUES (1, 0)`)
	if err == nil {
		t.Fatalf("expected check violation")
	}
	// We expose the constraint name in the error message — sqlc tests
	// often grep for it.
	if msg := err.Error(); !strings.Contains(msg, "qty_positive") {
		t.Errorf("error %q does not mention constraint name", msg)
	}
}
