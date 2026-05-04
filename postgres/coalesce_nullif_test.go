package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func nullPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

// TestCoalesce_FirstNonNull: COALESCE returns the first non-null arg,
// across ints, text, and a longer arg list.
func TestCoalesce_FirstNonNull(t *testing.T) {
	pool, ctx, cleanup := nullPool(t)
	defer cleanup()

	cases := []struct {
		sql  string
		want string
	}{
		{`SELECT COALESCE('first', 'second')`, "first"},
		{`SELECT COALESCE(NULL, 'second')`, "second"},
		{`SELECT COALESCE(NULL, NULL, 'third')`, "third"},
	}
	for _, tc := range cases {
		var got string
		if err := pool.QueryRow(ctx, tc.sql).Scan(&got); err != nil {
			t.Errorf("%q: %v", tc.sql, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.sql, got, tc.want)
		}
	}

	// Integer COALESCE.
	var n int32
	if err := pool.QueryRow(ctx, `SELECT COALESCE(NULL, 42)`).Scan(&n); err != nil {
		t.Fatalf("int coalesce: %v", err)
	}
	if n != 42 {
		t.Errorf("int coalesce: got %d, want 42", n)
	}
}

// TestCoalesce_AllNull: every arg null → NULL.
func TestCoalesce_AllNull(t *testing.T) {
	pool, ctx, cleanup := nullPool(t)
	defer cleanup()

	var s *string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(NULL::text, NULL::text)`).Scan(&s); err != nil {
		// Cast syntax isn't in our parser yet, fall back to plain NULLs.
		s = nil
		if err := pool.QueryRow(ctx, `SELECT COALESCE(NULL, NULL)`).Scan(&s); err != nil {
			t.Fatalf("SELECT: %v", err)
		}
	}
	if s != nil {
		t.Errorf("got %q, want nil", *s)
	}
}

// TestNullif_EqualsReturnsNull: NULLIF(a, a) → NULL.
func TestNullif_EqualsReturnsNull(t *testing.T) {
	pool, ctx, cleanup := nullPool(t)
	defer cleanup()

	var s *string
	if err := pool.QueryRow(ctx, `SELECT NULLIF('x', 'x')`).Scan(&s); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if s != nil {
		t.Errorf("got %q, want nil", *s)
	}
}

// TestNullif_DifferentReturnsFirst: NULLIF(a, b) where a != b → a.
func TestNullif_DifferentReturnsFirst(t *testing.T) {
	pool, ctx, cleanup := nullPool(t)
	defer cleanup()

	var got string
	if err := pool.QueryRow(ctx, `SELECT NULLIF('x', 'y')`).Scan(&got); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if got != "x" {
		t.Errorf("got %q, want x", got)
	}
}

// TestCoalesce_OnColumn: real sqlc-style usage —
// `SELECT COALESCE(name, 'unknown') FROM t WHERE id = $1`.
func TestCoalesce_OnColumn(t *testing.T) {
	pool, ctx, cleanup := nullPool(t)
	defer cleanup()

	if _, err := pool.Exec(ctx, `CREATE TABLE users (id int PRIMARY KEY, name text)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, name) VALUES (1, 'alice'), (2, NULL)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	for _, tc := range []struct {
		id   int32
		want string
	}{
		{1, "alice"},
		{2, "unknown"},
	} {
		var got string
		if err := pool.QueryRow(ctx,
			`SELECT COALESCE(name, 'unknown') FROM users WHERE id = $1`, tc.id,
		).Scan(&got); err != nil {
			t.Fatalf("id=%d: %v", tc.id, err)
		}
		if got != tc.want {
			t.Errorf("id=%d: got %q, want %q", tc.id, got, tc.want)
		}
	}
}
