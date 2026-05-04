package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func filterPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	if _, err := pool.Exec(ctx, `CREATE TABLE users (id int PRIMARY KEY, region text NOT NULL, active bool NOT NULL, score int NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, region, active, score) VALUES
			(1, 'east', true,  100),
			(2, 'east', false, 50),
			(3, 'east', true,  90),
			(4, 'west', true,  200),
			(5, 'west', false, 30)`,
	); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestAggFilter_CountWhereActive(t *testing.T) {
	pool, ctx, cleanup := filterPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `
		SELECT region,
		       count(*) FILTER (WHERE active) AS active_count,
		       count(*) AS total
		FROM users GROUP BY region ORDER BY region`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	type row struct {
		region             string
		activeCount, total int64
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.region, &r.activeCount, &r.total); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 groups", len(got))
	}
	if got[0].region != "east" || got[0].activeCount != 2 || got[0].total != 3 {
		t.Errorf("east: %+v", got[0])
	}
	if got[1].region != "west" || got[1].activeCount != 1 || got[1].total != 2 {
		t.Errorf("west: %+v", got[1])
	}
}

func TestAggFilter_SumPositive(t *testing.T) {
	pool, ctx, cleanup := filterPool(t)
	defer cleanup()
	var got int64
	if err := pool.QueryRow(ctx,
		`SELECT sum(score) FILTER (WHERE active) FROM users`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	// Active rows: 100 + 90 + 200 = 390.
	if got != 390 {
		t.Errorf("got %d, want 390", got)
	}
}

func TestAggFilter_NullFilterSkips(t *testing.T) {
	pool, ctx, cleanup := filterPool(t)
	defer cleanup()
	// `score > NULL` is NULL → row excluded. Result is sum over an
	// empty set, which is NULL.
	var got *int64
	if err := pool.QueryRow(ctx,
		`SELECT sum(score) FILTER (WHERE score > NULL) FROM users`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != nil {
		t.Errorf("got %d, want NULL", *got)
	}
}

// FILTER + DISTINCT compose: dedupe THEN filter.
func TestAggFilter_WithDistinct(t *testing.T) {
	pool, ctx, cleanup := filterPool(t)
	defer cleanup()
	var got int64
	if err := pool.QueryRow(ctx,
		`SELECT count(DISTINCT score) FILTER (WHERE active) FROM users`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	// Distinct active scores: 100, 90, 200 → 3.
	if got != 3 {
		t.Errorf("got %d, want 3", got)
	}
}
