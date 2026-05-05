package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func intervalCodecPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

func TestIntervalCodec_AgeRoundTrip(t *testing.T) {
	pool, ctx, cleanup := intervalCodecPool(t)
	defer cleanup()
	var got time.Duration
	if err := pool.QueryRow(ctx, `SELECT age(make_timestamp(2026, 1, 2, 0, 0, 0), make_timestamp(2026, 1, 1, 0, 0, 0))`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != 24*time.Hour {
		t.Errorf("got %v, want 24h", got)
	}
}

func TestIntervalCodec_Literal(t *testing.T) {
	pool, ctx, cleanup := intervalCodecPool(t)
	defer cleanup()
	var got time.Duration
	if err := pool.QueryRow(ctx, `SELECT interval '2 hours 30 minutes'`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	want := 2*time.Hour + 30*time.Minute
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestIntervalCodec_SubSecond(t *testing.T) {
	pool, ctx, cleanup := intervalCodecPool(t)
	defer cleanup()
	var got time.Duration
	if err := pool.QueryRow(ctx, `SELECT age(make_timestamp(2026, 1, 1, 0, 0, 1), make_timestamp(2026, 1, 1, 0, 0, 0))`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != time.Second {
		t.Errorf("got %v, want 1s", got)
	}
}

func TestIntervalCodec_Negative(t *testing.T) {
	pool, ctx, cleanup := intervalCodecPool(t)
	defer cleanup()
	var got time.Duration
	if err := pool.QueryRow(ctx, `SELECT age(make_timestamp(2026, 1, 1, 0, 0, 0), make_timestamp(2026, 1, 2, 0, 0, 0))`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != -24*time.Hour {
		t.Errorf("got %v, want -24h", got)
	}
}
