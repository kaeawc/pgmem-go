package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func textFuncsPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

func TestTrim(t *testing.T) {
	pool, ctx, cleanup := textFuncsPool(t)
	defer cleanup()
	cases := []struct {
		in, want string
	}{
		{"  spaces  ", "spaces"},
		{"\t\nmixed\r\n", "mixed"},
		{"clean", "clean"},
		{"", ""},
	}
	for _, tc := range cases {
		var got string
		if err := pool.QueryRow(ctx, `SELECT trim($1)`, tc.in).Scan(&got); err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("trim(%q): got %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestReplace(t *testing.T) {
	pool, ctx, cleanup := textFuncsPool(t)
	defer cleanup()
	var got string
	if err := pool.QueryRow(ctx, `SELECT replace('aaa-bbb-ccc', '-', '_')`).Scan(&got); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if got != "aaa_bbb_ccc" {
		t.Errorf("got %q, want aaa_bbb_ccc", got)
	}

	// Removal (replace with empty string).
	if err := pool.QueryRow(ctx, `SELECT replace('hello world', ' ', '')`).Scan(&got); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if got != "helloworld" {
		t.Errorf("got %q, want helloworld", got)
	}
}

func TestSubstring_TwoAndThreeArg(t *testing.T) {
	pool, ctx, cleanup := textFuncsPool(t)
	defer cleanup()

	cases := []struct {
		sql  string
		want string
	}{
		{`SELECT substring('hello world', 7)`, "world"},
		{`SELECT substring('hello world', 1, 5)`, "hello"},
		{`SELECT substring('héllo', 2, 3)`, "éll"}, // code-point indexing
		// PG clamps: from <= 0 starts at 1; over-long count clamps to end.
		{`SELECT substring('abc', 0, 2)`, "a"},
		{`SELECT substring('abc', 2, 100)`, "bc"},
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

// Negative-length substring rejection is exercised at the exec level
// in exec/funcs_substring_test.go — we can't get a negative literal
// through this layer until the parser learns unary minus.
//
// (TestSubstring_NegativeLengthErrors lives in the exec package now.)

func TestStrpos(t *testing.T) {
	pool, ctx, cleanup := textFuncsPool(t)
	defer cleanup()
	cases := []struct {
		sql  string
		want int32
	}{
		{`SELECT strpos('hello world', 'world')`, 7},
		{`SELECT strpos('hello world', 'xyz')`, 0},
		{`SELECT strpos('hello world', '')`, 1},      // empty needle → 1
		{`SELECT strpos('héllo world', 'world')`, 7}, // code-point indexing
	}
	for _, tc := range cases {
		var got int32
		if err := pool.QueryRow(ctx, tc.sql).Scan(&got); err != nil {
			t.Errorf("%q: %v", tc.sql, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%q: got %d, want %d", tc.sql, got, tc.want)
		}
	}
}

func TestStringFunctions_NullArgsReturnNull(t *testing.T) {
	pool, ctx, cleanup := textFuncsPool(t)
	defer cleanup()
	for _, sql := range []string{
		`SELECT trim(NULL::text)`,
		`SELECT replace(NULL, 'a', 'b')`,
		`SELECT substring(NULL, 1, 2)`,
		`SELECT strpos(NULL, 'a')`,
	} {
		// Cast syntax not in scope for the parser; fall back to the
		// no-cast forms which still propagate NULL through the
		// same code path.
		s := sql
		switch sql {
		case `SELECT trim(NULL::text)`:
			s = `SELECT trim(NULL)`
		}
		// We can't easily Scan into a typed-but-nullable destination
		// without cast support, so accept any Scan that yields nil and
		// any scan error that's just a nil destination mismatch.
		var dest *string
		err := pool.QueryRow(ctx, s).Scan(&dest)
		if err != nil {
			// strpos returns int4; try int destination.
			var n *int32
			if err2 := pool.QueryRow(ctx, s).Scan(&n); err2 != nil {
				t.Errorf("%q: %v / %v", s, err, err2)
				continue
			}
			if n != nil {
				t.Errorf("%q: got %d, want nil", s, *n)
			}
			continue
		}
		if dest != nil {
			t.Errorf("%q: got %q, want nil", s, *dest)
		}
	}
}
