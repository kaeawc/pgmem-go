package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

// TestSetNow_PinsClockForBuiltins: after SetNow(t), now() returns t
// instead of the wall clock. Useful for sqlc test code that asserts
// exact timestamp values.
func TestSetNow_PinsClockForBuiltins(t *testing.T) {
	srv, err := postgres.Start(t)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	pinned := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	srv.SetNow(pinned)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, srv.DSN())
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	var got time.Time
	if err := pool.QueryRow(ctx, `SELECT now()`).Scan(&got); err != nil {
		t.Fatalf("SELECT now: %v", err)
	}
	if !got.Equal(pinned) {
		t.Errorf("now(): got %v, want %v", got, pinned)
	}
}

// TestSetNow_ZeroTimeRevertsToWallClock: passing the zero time.Time
// disables pinning and now() goes back to wall clock.
func TestSetNow_ZeroTimeRevertsToWallClock(t *testing.T) {
	srv, err := postgres.Start(t)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	pinned := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	srv.SetNow(pinned)
	srv.SetNow(time.Time{}) // unpin

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, srv.DSN())
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	before := time.Now().UTC()
	var got time.Time
	if err := pool.QueryRow(ctx, `SELECT now()`).Scan(&got); err != nil {
		t.Fatalf("SELECT now: %v", err)
	}
	after := time.Now().UTC()
	if got.Before(before) || got.After(after.Add(time.Second)) {
		t.Errorf("now() after unpin: %v not in [%v, %v]", got, before, after)
	}
}

// TestSetNow_TwoServersIndependent: two pgmem instances pin clocks
// independently — confirms the clock isn't a package-level global.
func TestSetNow_TwoServersIndependent(t *testing.T) {
	srvA, err := postgres.Start(t)
	if err != nil {
		t.Fatalf("Start A: %v", err)
	}
	srvB, err := postgres.Start(t)
	if err != nil {
		t.Fatalf("Start B: %v", err)
	}
	tA := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	tB := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	srvA.SetNow(tA)
	srvB.SetNow(tB)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for i, c := range []struct {
		dsn  string
		want time.Time
	}{
		{srvA.DSN(), tA},
		{srvB.DSN(), tB},
	} {
		pool, err := pgxpool.New(ctx, c.dsn)
		if err != nil {
			t.Fatalf("server %d: %v", i, err)
		}
		var got time.Time
		if err := pool.QueryRow(ctx, `SELECT now()`).Scan(&got); err != nil {
			t.Fatalf("server %d: %v", i, err)
		}
		pool.Close()
		if !got.Equal(c.want) {
			t.Errorf("server %d: got %v, want %v", i, got, c.want)
		}
	}
}
