package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func extractPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

func TestExtract_YearMonthDay(t *testing.T) {
	pool, ctx, cleanup := extractPool(t)
	defer cleanup()
	ts := time.Date(2024, 7, 15, 13, 45, 0, 0, time.UTC)
	var year, month, day int64
	if err := pool.QueryRow(ctx,
		`SELECT extract(year from $1::timestamptz), extract(month from $1::timestamptz), extract(day from $1::timestamptz)`,
		ts,
	).Scan(&year, &month, &day); err != nil {
		t.Fatalf("query: %v", err)
	}
	if year != 2024 || month != 7 || day != 15 {
		t.Errorf("got y=%d m=%d d=%d, want 2024/7/15", year, month, day)
	}
}

func TestExtract_HourMinuteSecond(t *testing.T) {
	pool, ctx, cleanup := extractPool(t)
	defer cleanup()
	ts := time.Date(2024, 7, 15, 13, 45, 30, 0, time.UTC)
	var h, m, s int64
	if err := pool.QueryRow(ctx,
		`SELECT extract(hour from $1::timestamptz), extract(minute from $1::timestamptz), extract(second from $1::timestamptz)`,
		ts,
	).Scan(&h, &m, &s); err != nil {
		t.Fatalf("query: %v", err)
	}
	if h != 13 || m != 45 || s != 30 {
		t.Errorf("got h=%d m=%d s=%d, want 13/45/30", h, m, s)
	}
}

func TestExtract_Epoch(t *testing.T) {
	pool, ctx, cleanup := extractPool(t)
	defer cleanup()
	ts := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	var got int64
	if err := pool.QueryRow(ctx,
		`SELECT extract(epoch from $1::timestamptz)`, ts,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	want := ts.Unix()
	if got != want {
		t.Errorf("got %d, want %d", got, want)
	}
}

func TestDatePart_FunctionForm(t *testing.T) {
	pool, ctx, cleanup := extractPool(t)
	defer cleanup()
	ts := time.Date(2024, 7, 15, 0, 0, 0, 0, time.UTC)
	var got int64
	if err := pool.QueryRow(ctx,
		`SELECT date_part('year', $1::timestamptz)`, ts,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != 2024 {
		t.Errorf("got %d, want 2024", got)
	}
}

func TestExtract_NullPropagates(t *testing.T) {
	pool, ctx, cleanup := extractPool(t)
	defer cleanup()
	var got *int64
	if err := pool.QueryRow(ctx,
		`SELECT extract(year from NULL::timestamptz)`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != nil {
		t.Errorf("got %d, want NULL", *got)
	}
}

func TestExtract_UnknownFieldErrors(t *testing.T) {
	pool, ctx, cleanup := extractPool(t)
	defer cleanup()
	var got int64
	err := pool.QueryRow(ctx,
		`SELECT extract(nonsense from $1::timestamptz)`,
		time.Now().UTC(),
	).Scan(&got)
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}
