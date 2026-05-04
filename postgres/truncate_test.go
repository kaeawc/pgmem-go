package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func truncatePool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
		`CREATE TABLE a (id int PRIMARY KEY, n int NOT NULL)`,
		`CREATE TABLE b (id int PRIMARY KEY, n int NOT NULL)`,
		`INSERT INTO a (id, n) VALUES (1, 10), (2, 20)`,
		`INSERT INTO b (id, n) VALUES (3, 30), (4, 40)`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func count(t *testing.T, pool *pgxpool.Pool, ctx context.Context, table string) int64 {
	t.Helper()
	var got int64
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&got); err != nil {
		t.Fatalf("count(%s): %v", table, err)
	}
	return got
}

func TestTruncate_SingleTable(t *testing.T) {
	pool, ctx, cleanup := truncatePool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `TRUNCATE a`); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	if got := count(t, pool, ctx, "a"); got != 0 {
		t.Errorf("a: got %d rows, want 0", got)
	}
	if got := count(t, pool, ctx, "b"); got != 2 {
		t.Errorf("b: got %d rows, want 2 (untouched)", got)
	}
}

func TestTruncate_TableKeyword(t *testing.T) {
	pool, ctx, cleanup := truncatePool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `TRUNCATE TABLE a`); err != nil {
		t.Fatalf("TRUNCATE TABLE: %v", err)
	}
	if got := count(t, pool, ctx, "a"); got != 0 {
		t.Errorf("a: got %d rows, want 0", got)
	}
}

func TestTruncate_Multiple(t *testing.T) {
	pool, ctx, cleanup := truncatePool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `TRUNCATE a, b RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	if got := count(t, pool, ctx, "a"); got != 0 {
		t.Errorf("a: got %d rows, want 0", got)
	}
	if got := count(t, pool, ctx, "b"); got != 0 {
		t.Errorf("b: got %d rows, want 0", got)
	}
}

func TestTruncate_UnknownTable(t *testing.T) {
	pool, ctx, cleanup := truncatePool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `TRUNCATE nonexistent`); err == nil {
		t.Fatal("expected error for unknown table")
	}
}
