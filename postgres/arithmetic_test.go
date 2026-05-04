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

// TestArithmetic_BasicOps: + - * / % over int4 work in SELECT.
func TestArithmetic_BasicOps(t *testing.T) {
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

	cases := []struct {
		sql  string
		want int32
	}{
		{`SELECT 2 + 3`, 5},
		{`SELECT 10 - 4`, 6},
		{`SELECT 6 * 7`, 42},
		{`SELECT 20 / 4`, 5},
		{`SELECT 17 % 5`, 2},
		{`SELECT 1 + 2 * 3`, 7}, // precedence
		{`SELECT (1 + 2) * 3`, 9},
		{`SELECT 10 - 4 - 1`, 5}, // left-assoc
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

// TestArithmetic_Update_IncrementCounter is the canonical sqlc pattern
// `UPDATE t SET n = n + 1`.
func TestArithmetic_Update_IncrementCounter(t *testing.T) {
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

	if _, err := pool.Exec(ctx, `CREATE TABLE counters (id int PRIMARY KEY, n int NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO counters (id, n) VALUES (1, 0)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := pool.Exec(ctx, `UPDATE counters SET n = n + 1 WHERE id = 1`); err != nil {
			t.Fatalf("UPDATE %d: %v", i, err)
		}
	}
	var n int32
	if err := pool.QueryRow(ctx, `SELECT n FROM counters WHERE id = 1`).Scan(&n); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if n != 5 {
		t.Errorf("counter: got %d, want 5", n)
	}
}

// TestArithmetic_DivisionByZero surfaces SQLSTATE 22012.
func TestArithmetic_DivisionByZero(t *testing.T) {
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

	_, err = pool.Exec(ctx, `SELECT 10 / 0`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "22012" {
		t.Errorf("got %v, want 22012", err)
	}
}

// TestArithmetic_BigintWidens: int8 + int4 → int8. pgx scans as int64.
func TestArithmetic_BigintWidens(t *testing.T) {
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

	if _, err := pool.Exec(ctx, `CREATE TABLE big (a bigint NOT NULL, b int NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO big (a, b) VALUES (1000000000000, 5)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	var sum int64
	if err := pool.QueryRow(ctx, `SELECT a + b FROM big`).Scan(&sum); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if sum != 1000000000005 {
		t.Errorf("sum: got %d, want 1000000000005", sum)
	}
}
