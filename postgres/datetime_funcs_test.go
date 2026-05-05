package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func dtPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

func TestMakeDate(t *testing.T) {
	pool, ctx, cleanup := dtPool(t)
	defer cleanup()
	var got time.Time
	if err := pool.QueryRow(ctx, `SELECT make_date(2026, 5, 4)`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	want := time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMakeTime(t *testing.T) {
	pool, ctx, cleanup := dtPool(t)
	defer cleanup()
	var got time.Time
	if err := pool.QueryRow(ctx, `SELECT make_time(13, 45, 30)`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	got = got.UTC()
	if got.Hour() != 13 || got.Minute() != 45 || got.Second() != 30 {
		t.Errorf("got %v, want 13:45:30 UTC", got)
	}
}

func TestMakeTimestamp(t *testing.T) {
	pool, ctx, cleanup := dtPool(t)
	defer cleanup()
	var got time.Time
	if err := pool.QueryRow(ctx, `SELECT make_timestamp(2026, 5, 4, 12, 0, 0)`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	want := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestToTimestamp_UnixSeconds(t *testing.T) {
	pool, ctx, cleanup := dtPool(t)
	defer cleanup()
	var got time.Time
	if err := pool.QueryRow(ctx, `SELECT to_timestamp(1735689600)`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	// 1735689600 = 2025-01-01 00:00:00 UTC
	want := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestAge_TwoArgs(t *testing.T) {
	pool, ctx, cleanup := dtPool(t)
	defer cleanup()
	// We don't yet support binary-encoding interval over the wire,
	// so test age() through arithmetic: age(t1, t2) = t1 - t2, so
	// `t2 + age(t1, t2)` should equal t1.
	var got time.Time
	if err := pool.QueryRow(ctx, `SELECT make_timestamp(2026, 1, 1, 0, 0, 0) + age(make_timestamp(2026, 1, 2, 0, 0, 0), make_timestamp(2026, 1, 1, 0, 0, 0))`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	want := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestAge_OneArg(t *testing.T) {
	pool, ctx, cleanup := dtPool(t)
	defer cleanup()
	// One-arg age uses today's UTC midnight as the reference.
	// Composing it through arithmetic ensures we exercise the
	// computation without returning the interval directly.
	var got time.Time
	if err := pool.QueryRow(ctx, `SELECT current_date + age(make_timestamp(2020, 1, 1, 0, 0, 0))`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	want := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMakeTimestamp_BadArity(t *testing.T) {
	pool, ctx, cleanup := dtPool(t)
	defer cleanup()
	var got time.Time
	if err := pool.QueryRow(ctx, `SELECT make_timestamp(2026, 5, 4)`).Scan(&got); err == nil {
		t.Fatalf("expected arity error")
	}
}
