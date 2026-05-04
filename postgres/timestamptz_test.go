package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

// TestTimestamptz_NowRoundTrip stores now() and reads it back.
// Verifies the binary codec across the wire and that the value is
// "approximately now" (PG's now() is the start of the current
// transaction; we approximate with wall-clock at eval time).
func TestTimestamptz_NowRoundTrip(t *testing.T) {
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

	if _, err := pool.Exec(ctx, `CREATE TABLE events (id int PRIMARY KEY, at timestamptz NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	before := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO events (id, at) VALUES (1, now())`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	after := time.Now().UTC()

	var got time.Time
	if err := pool.QueryRow(ctx, `SELECT at FROM events WHERE id = 1`).Scan(&got); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if got.Before(before) || got.After(after.Add(time.Second)) {
		t.Errorf("at: %v not in [%v, %v]", got, before, after)
	}
}

// TestTimestamptz_AcceptsExplicitValue confirms a client-supplied
// time.Time round-trips through Bind → INSERT → SELECT → Scan.
func TestTimestamptz_AcceptsExplicitValue(t *testing.T) {
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

	if _, err := pool.Exec(ctx, `CREATE TABLE events (id int PRIMARY KEY, at timestamptz NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}

	want := time.Date(2025, 1, 15, 14, 30, 45, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO events (id, at) VALUES ($1, $2)`, int32(1), want); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	var got time.Time
	if err := pool.QueryRow(ctx, `SELECT at FROM events WHERE id = 1`).Scan(&got); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestTimestamptz_OrderByWorks confirms compareValues handles time.Time
// so ORDER BY at column does the right thing.
func TestTimestamptz_OrderByWorks(t *testing.T) {
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

	if _, err := pool.Exec(ctx, `CREATE TABLE events (id int PRIMARY KEY, at timestamptz NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
	for i, ts := range []time.Time{t3, t1, t2} {
		if _, err := pool.Exec(ctx, `INSERT INTO events (id, at) VALUES ($1, $2)`, int32(i+1), ts); err != nil {
			t.Fatalf("INSERT %d: %v", i, err)
		}
	}
	rows, err := pool.Query(ctx, `SELECT at FROM events ORDER BY at ASC`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	var got []time.Time
	for rows.Next() {
		var ts time.Time
		_ = rows.Scan(&ts)
		got = append(got, ts)
	}
	if len(got) != 3 || !got[0].Equal(t1) || !got[1].Equal(t2) || !got[2].Equal(t3) {
		t.Errorf("ordered: got %v, want [t1 t2 t3]", got)
	}
}
