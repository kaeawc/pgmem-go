package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func stringsPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

// TestStringConcat_BasicAndImplicitCast: string||string and
// string||int both work — the latter follows PG's implicit cast to
// text on the integer side.
func TestStringConcat_BasicAndImplicitCast(t *testing.T) {
	pool, ctx, cleanup := stringsPool(t)
	defer cleanup()

	cases := []struct {
		sql  string
		want string
	}{
		{`SELECT 'hello, ' || 'world'`, "hello, world"},
		{`SELECT 'count: ' || 42`, "count: 42"},
		{`SELECT 'a' || 'b' || 'c'`, "abc"}, // left-associative
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
}

// TestStringConcat_NullPropagates: NULL on either side produces NULL.
func TestStringConcat_NullPropagates(t *testing.T) {
	pool, ctx, cleanup := stringsPool(t)
	defer cleanup()

	var got *string
	if err := pool.QueryRow(ctx, `SELECT 'x' || NULL`).Scan(&got); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if got != nil {
		t.Errorf("got %q, want nil", *got)
	}
}

// TestStringFunctions_LowerUpperLength covers the three string
// builtins this PR adds.
func TestStringFunctions_LowerUpperLength(t *testing.T) {
	pool, ctx, cleanup := stringsPool(t)
	defer cleanup()

	type stringCase struct {
		sql  string
		want string
	}
	for _, tc := range []stringCase{
		{`SELECT lower('Hello WORLD')`, "hello world"},
		{`SELECT upper('Hello WORLD')`, "HELLO WORLD"},
	} {
		var got string
		if err := pool.QueryRow(ctx, tc.sql).Scan(&got); err != nil {
			t.Errorf("%q: %v", tc.sql, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.sql, got, tc.want)
		}
	}

	// length() returns int4 and counts code points (not bytes).
	var n int32
	if err := pool.QueryRow(ctx, `SELECT length('héllo')`).Scan(&n); err != nil {
		t.Fatalf("length: %v", err)
	}
	if n != 5 {
		t.Errorf("length: got %d, want 5 (code points)", n)
	}
}

// TestStringFunctions_NullIsNull: lower/upper/length on NULL → NULL.
func TestStringFunctions_NullIsNull(t *testing.T) {
	pool, ctx, cleanup := stringsPool(t)
	defer cleanup()

	for _, sql := range []string{
		`SELECT lower(NULL)`,
		`SELECT upper(NULL)`,
		`SELECT length(NULL)`,
	} {
		var s *string
		if err := pool.QueryRow(ctx, sql).Scan(&s); err != nil {
			// length returns int, so try int destination if string scan failed.
			var n *int32
			if err2 := pool.QueryRow(ctx, sql).Scan(&n); err2 != nil {
				t.Errorf("%q: %v / %v", sql, err, err2)
				continue
			}
			if n != nil {
				t.Errorf("%q: got %d, want nil", sql, *n)
			}
			continue
		}
		if s != nil {
			t.Errorf("%q: got %q, want nil", sql, *s)
		}
	}
}

// TestStringConcat_OnColumns: || over actual column data.
func TestStringConcat_OnColumns(t *testing.T) {
	pool, ctx, cleanup := stringsPool(t)
	defer cleanup()

	if _, err := pool.Exec(ctx, `CREATE TABLE people (id int PRIMARY KEY, first text NOT NULL, last text NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO people (id, first, last) VALUES (1, 'Ada', 'Lovelace')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	var got string
	if err := pool.QueryRow(ctx, `SELECT first || ' ' || last FROM people WHERE id = 1`).Scan(&got); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if got != "Ada Lovelace" {
		t.Errorf("got %q, want %q", got, "Ada Lovelace")
	}
}
