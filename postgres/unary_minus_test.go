package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func unaryMinusPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

func TestUnaryMinus_Literal(t *testing.T) {
	pool, ctx, cleanup := unaryMinusPool(t)
	defer cleanup()
	cases := []struct {
		sql  string
		want int32
	}{
		{`SELECT -1`, -1},
		{`SELECT -42`, -42},
		{`SELECT -(1 + 2)`, -3},
		{`SELECT - -5`, 5},   // double-negate
		{`SELECT -1 + 5`, 4}, // unary tighter than binary
		{`SELECT 5 - -3`, 8}, // binary minus then unary minus
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

func TestUnaryMinus_OnColumn(t *testing.T) {
	pool, ctx, cleanup := unaryMinusPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE nums (n int NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO nums (n) VALUES (10)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	var got int32
	if err := pool.QueryRow(ctx, `SELECT -n FROM nums`).Scan(&got); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if got != -10 {
		t.Errorf("got %d, want -10", got)
	}
}

// TestSubstring_NegativeLengthErrors_LiteralNow uses the unary-minus
// path that PR #25 had to skip. Previously the parser blew up on the
// negative literal entirely; now it parses and the substring builtin
// rejects it with SQLSTATE 22011.
func TestSubstring_NegativeLengthErrors_Literal(t *testing.T) {
	pool, ctx, cleanup := unaryMinusPool(t)
	defer cleanup()
	_, err := pool.Exec(ctx, `SELECT substring('abc', 1, -1)`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "22011" {
		t.Errorf("got %v, want 22011", err)
	}
}
