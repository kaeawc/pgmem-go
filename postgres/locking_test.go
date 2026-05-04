package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func lockPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	if _, err := pool.Exec(ctx, `CREATE TABLE jobs (id int PRIMARY KEY, status text NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO jobs (id, status) VALUES (1, 'queued'), (2, 'queued'), (3, 'done')`,
	); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func runIDQuery(t *testing.T, pool *pgxpool.Pool, ctx context.Context, sql string) []int32 {
	t.Helper()
	rows, err := pool.Query(ctx, sql)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	defer rows.Close()
	var got []int32
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, id)
	}
	return got
}

func TestLocking_ForUpdate(t *testing.T) {
	pool, ctx, cleanup := lockPool(t)
	defer cleanup()
	got := runIDQuery(t, pool, ctx,
		`SELECT id FROM jobs WHERE status = 'queued' ORDER BY id FOR UPDATE`)
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("got %v, want [1 2]", got)
	}
}

func TestLocking_ForUpdateSkipLocked(t *testing.T) {
	pool, ctx, cleanup := lockPool(t)
	defer cleanup()
	got := runIDQuery(t, pool, ctx,
		`SELECT id FROM jobs WHERE status = 'queued' ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED`)
	if len(got) != 1 || got[0] != 1 {
		t.Errorf("got %v, want [1]", got)
	}
}

func TestLocking_ForShare(t *testing.T) {
	pool, ctx, cleanup := lockPool(t)
	defer cleanup()
	got := runIDQuery(t, pool, ctx, `SELECT id FROM jobs ORDER BY id FOR SHARE`)
	if len(got) != 3 {
		t.Errorf("got %d rows, want 3", len(got))
	}
}

func TestLocking_ForNoKeyUpdate(t *testing.T) {
	pool, ctx, cleanup := lockPool(t)
	defer cleanup()
	got := runIDQuery(t, pool, ctx,
		`SELECT id FROM jobs WHERE id = 1 FOR NO KEY UPDATE`)
	if len(got) != 1 || got[0] != 1 {
		t.Errorf("got %v, want [1]", got)
	}
}

func TestLocking_ForKeyShareOf(t *testing.T) {
	pool, ctx, cleanup := lockPool(t)
	defer cleanup()
	got := runIDQuery(t, pool, ctx,
		`SELECT id FROM jobs WHERE id = 2 FOR KEY SHARE OF jobs NOWAIT`)
	if len(got) != 1 || got[0] != 2 {
		t.Errorf("got %v, want [2]", got)
	}
}
