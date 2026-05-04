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

func mathPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

func TestAbs(t *testing.T) {
	pool, ctx, cleanup := mathPool(t)
	defer cleanup()
	cases := []struct {
		in, want int32
	}{{-7, 7}, {0, 0}, {42, 42}}
	for _, c := range cases {
		var got int32
		if err := pool.QueryRow(ctx, `SELECT abs($1::int)`, c.in).Scan(&got); err != nil {
			t.Fatalf("abs(%d): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("abs(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestMod_PG(t *testing.T) {
	pool, ctx, cleanup := mathPool(t)
	defer cleanup()
	var got int32
	if err := pool.QueryRow(ctx, `SELECT mod(10, 3)::int`).Scan(&got); err != nil {
		t.Fatalf("mod(10,3): %v", err)
	}
	if got != 1 {
		t.Errorf("mod(10,3) = %d, want 1", got)
	}
	// Sign follows dividend.
	if err := pool.QueryRow(ctx, `SELECT mod(-7, 3)::int`).Scan(&got); err != nil {
		t.Fatalf("mod(-7,3): %v", err)
	}
	if got != -1 {
		t.Errorf("mod(-7,3) = %d, want -1", got)
	}
}

func TestMod_DivByZero(t *testing.T) {
	pool, ctx, cleanup := mathPool(t)
	defer cleanup()
	var got int64
	err := pool.QueryRow(ctx, `SELECT mod(10, 0)`).Scan(&got)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "22012" {
		t.Errorf("mod(10,0): got %v, want SQLSTATE 22012", err)
	}
}

func TestGreatestLeast(t *testing.T) {
	pool, ctx, cleanup := mathPool(t)
	defer cleanup()
	var hi, lo int32
	if err := pool.QueryRow(ctx,
		`SELECT greatest(3, 7, 1, 5)::int, least(3, 7, 1, 5)::int`,
	).Scan(&hi, &lo); err != nil {
		t.Fatalf("greatest/least: %v", err)
	}
	if hi != 7 || lo != 1 {
		t.Errorf("greatest=%d least=%d, want 7 / 1", hi, lo)
	}
}

func TestGreatest_NullsSkipped(t *testing.T) {
	pool, ctx, cleanup := mathPool(t)
	defer cleanup()
	var got int32
	if err := pool.QueryRow(ctx,
		`SELECT greatest(NULL, 3, NULL, 9)::int`,
	).Scan(&got); err != nil {
		t.Fatalf("greatest with NULLs: %v", err)
	}
	if got != 9 {
		t.Errorf("greatest = %d, want 9", got)
	}
}

func TestGreatest_AllNullsIsNull(t *testing.T) {
	pool, ctx, cleanup := mathPool(t)
	defer cleanup()
	var got *int32
	if err := pool.QueryRow(ctx,
		`SELECT greatest(NULL::int, NULL::int)`,
	).Scan(&got); err != nil {
		t.Fatalf("greatest all-NULL: %v", err)
	}
	if got != nil {
		t.Errorf("got %d, want NULL", *got)
	}
}
