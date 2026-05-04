package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func dateTruncPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

func TestDateTrunc_Day(t *testing.T) {
	pool, ctx, cleanup := dateTruncPool(t)
	defer cleanup()
	ts := time.Date(2024, 7, 15, 13, 45, 30, 0, time.UTC)
	var got time.Time
	if err := pool.QueryRow(ctx,
		`SELECT date_trunc('day', $1::timestamptz)`, ts,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	want := time.Date(2024, 7, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDateTrunc_Month(t *testing.T) {
	pool, ctx, cleanup := dateTruncPool(t)
	defer cleanup()
	ts := time.Date(2024, 7, 15, 13, 45, 30, 0, time.UTC)
	var got time.Time
	if err := pool.QueryRow(ctx,
		`SELECT date_trunc('month', $1::timestamptz)`, ts,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	want := time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDateTrunc_Hour(t *testing.T) {
	pool, ctx, cleanup := dateTruncPool(t)
	defer cleanup()
	ts := time.Date(2024, 7, 15, 13, 45, 30, 0, time.UTC)
	var got time.Time
	if err := pool.QueryRow(ctx,
		`SELECT date_trunc('hour', $1::timestamptz)`, ts,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	want := time.Date(2024, 7, 15, 13, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDateTrunc_Week_AnchorsToMonday(t *testing.T) {
	pool, ctx, cleanup := dateTruncPool(t)
	defer cleanup()
	// 2024-07-15 was a Monday already.
	mon := time.Date(2024, 7, 15, 13, 45, 30, 0, time.UTC)
	var got time.Time
	if err := pool.QueryRow(ctx,
		`SELECT date_trunc('week', $1::timestamptz)`, mon,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	want := time.Date(2024, 7, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// Sunday rolls back to the previous Monday.
	sun := time.Date(2024, 7, 21, 12, 0, 0, 0, time.UTC) // Sunday
	if err := pool.QueryRow(ctx,
		`SELECT date_trunc('week', $1::timestamptz)`, sun,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	want = time.Date(2024, 7, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Sunday: got %v, want %v", got, want)
	}
}

func TestDateTrunc_NullPropagates(t *testing.T) {
	pool, ctx, cleanup := dateTruncPool(t)
	defer cleanup()
	var got *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT date_trunc('day', NULL::timestamptz)`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != nil {
		t.Errorf("got %v, want NULL", *got)
	}
}
