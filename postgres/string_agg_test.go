package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func stringAggPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	for _, sql := range []string{
		`CREATE TABLE tags (id int PRIMARY KEY, name text NOT NULL, kind text NOT NULL)`,
		`INSERT INTO tags (id, name, kind) VALUES (1, 'red', 'color'), (2, 'blue', 'color'), (3, 'fast', 'speed')`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestStringAgg_Scalar(t *testing.T) {
	pool, ctx, cleanup := stringAggPool(t)
	defer cleanup()
	var got string
	if err := pool.QueryRow(ctx,
		`SELECT string_agg(name, ',') FROM tags`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	// Order isn't guaranteed without ORDER BY, but the joined string
	// must contain all three names separated by commas.
	expected := map[string]bool{
		"red,blue,fast": true,
		"red,fast,blue": true,
		"blue,red,fast": true,
		"blue,fast,red": true,
		"fast,red,blue": true,
		"fast,blue,red": true,
	}
	if !expected[got] {
		t.Errorf("got %q, want a permutation of red,blue,fast", got)
	}
}

func TestStringAgg_Grouped(t *testing.T) {
	pool, ctx, cleanup := stringAggPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx,
		`SELECT kind, string_agg(name, '|') FROM tags GROUP BY kind ORDER BY kind`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	type pair struct {
		kind, names string
	}
	var got []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.kind, &p.names); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, p)
	}
	if len(got) != 2 {
		t.Fatalf("got %d groups, want 2", len(got))
	}
	if got[0].kind != "color" {
		t.Errorf("kind[0] = %q, want color", got[0].kind)
	}
	// "red|blue" or "blue|red" — group order isn't guaranteed.
	if got[0].names != "red|blue" && got[0].names != "blue|red" {
		t.Errorf("color names = %q, want red|blue or blue|red", got[0].names)
	}
	if got[1].kind != "speed" || got[1].names != "fast" {
		t.Errorf("speed group: got %v, want {speed, fast}", got[1])
	}
}

// All-NULL input → NULL result (matches PG).
func TestStringAgg_AllNullsIsNull(t *testing.T) {
	pool, ctx, cleanup := stringAggPool(t)
	defer cleanup()
	var got *string
	if err := pool.QueryRow(ctx,
		`SELECT string_agg(name, ',') FROM tags WHERE id < 0`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != nil {
		t.Errorf("got %q, want NULL", *got)
	}
}
