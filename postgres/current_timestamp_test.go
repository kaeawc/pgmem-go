package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func currentPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
	t.Helper()
	srv, err := postgres.Start(t)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	pinned := time.Date(2024, 7, 15, 12, 30, 45, 0, time.UTC)
	srv.SetNow(pinned)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	pool, err := pgxpool.New(ctx, srv.DSN())
	if err != nil {
		cancel()
		t.Fatalf("pgxpool.New: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestCurrentTimestamp_NoParens(t *testing.T) {
	pool, ctx, cleanup := currentPool(t)
	defer cleanup()
	var got time.Time
	if err := pool.QueryRow(ctx, `SELECT current_timestamp`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	want := time.Date(2024, 7, 15, 12, 30, 45, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCurrentDate_TruncatedToMidnight(t *testing.T) {
	pool, ctx, cleanup := currentPool(t)
	defer cleanup()
	var got time.Time
	if err := pool.QueryRow(ctx, `SELECT current_date`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	want := time.Date(2024, 7, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCurrentTime_DateAtEpoch(t *testing.T) {
	pool, ctx, cleanup := currentPool(t)
	defer cleanup()
	var got time.Time
	if err := pool.QueryRow(ctx, `SELECT current_time`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	want := time.Date(1970, 1, 1, 12, 30, 45, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCurrentTimestamp_AsDefault(t *testing.T) {
	pool, ctx, cleanup := currentPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx,
		`CREATE TABLE t (id int PRIMARY KEY, created timestamptz NOT NULL)`,
	); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO t (id, created) VALUES (1, current_timestamp)`,
	); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	var got time.Time
	if err := pool.QueryRow(ctx, `SELECT created FROM t WHERE id = 1`).Scan(&got); err != nil {
		t.Fatalf("readback: %v", err)
	}
	want := time.Date(2024, 7, 15, 12, 30, 45, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
