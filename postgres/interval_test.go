package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func intervalPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
	t.Helper()
	srv, err := postgres.Start(t)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	pinned := time.Date(2024, 7, 15, 12, 0, 0, 0, time.UTC)
	srv.SetNow(pinned)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	pool, err := pgxpool.New(ctx, srv.DSN())
	if err != nil {
		cancel()
		t.Fatalf("pgxpool.New: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestInterval_AddToTimestamp(t *testing.T) {
	pool, ctx, cleanup := intervalPool(t)
	defer cleanup()
	var got time.Time
	if err := pool.QueryRow(ctx,
		`SELECT now() + interval '1 day'`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	want := time.Date(2024, 7, 16, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestInterval_SubtractFromTimestamp(t *testing.T) {
	pool, ctx, cleanup := intervalPool(t)
	defer cleanup()
	var got time.Time
	if err := pool.QueryRow(ctx,
		`SELECT now() - interval '5 hours'`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	want := time.Date(2024, 7, 15, 7, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestInterval_MultiUnit(t *testing.T) {
	pool, ctx, cleanup := intervalPool(t)
	defer cleanup()
	var got time.Time
	if err := pool.QueryRow(ctx,
		`SELECT now() + interval '2 days 3 hours 15 minutes'`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	want := time.Date(2024, 7, 17, 15, 15, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestInterval_AsFilter(t *testing.T) {
	pool, ctx, cleanup := intervalPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE events (id int PRIMARY KEY, at timestamptz NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO events (id, at) VALUES (1, $1), (2, $2), (3, $3)`,
		time.Date(2024, 7, 14, 12, 0, 0, 0, time.UTC), // 1 day before pinned now
		time.Date(2024, 7, 15, 6, 0, 0, 0, time.UTC),  // 6h before
		time.Date(2024, 7, 15, 13, 0, 0, 0, time.UTC), // 1h after
	); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	rows, err := pool.Query(ctx,
		`SELECT id FROM events WHERE at > now() - interval '1 day' ORDER BY id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var ids []int32
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	want := []int32{2, 3}
	if len(ids) != len(want) || ids[0] != want[0] || ids[1] != want[1] {
		t.Errorf("got %v, want %v", ids, want)
	}
}
