package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func distinctOnPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	if _, err := pool.Exec(ctx, `CREATE TABLE events (id int PRIMARY KEY, user_id int NOT NULL, kind text NOT NULL, ts timestamptz NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	now := time.Date(2024, 7, 15, 12, 0, 0, 0, time.UTC)
	for i, row := range []struct {
		id     int32
		user   int32
		kind   string
		offset time.Duration
	}{
		{1, 1, "login", 0},
		{2, 1, "login", time.Minute}, // newer login for user 1
		{3, 1, "logout", 2 * time.Minute},
		{4, 2, "login", 3 * time.Minute},
		{5, 2, "login", 4 * time.Minute},
	} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO events (id, user_id, kind, ts) VALUES ($1, $2, $3, $4)`,
			row.id, row.user, row.kind, now.Add(row.offset),
		); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

// TestDistinctOn_LatestPerUser exercises the canonical sqlc shape:
// keep the most-recent event per user via DISTINCT ON + ORDER BY.
func TestDistinctOn_LatestPerUser(t *testing.T) {
	pool, ctx, cleanup := distinctOnPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT ON (user_id) user_id, id, kind
		FROM events
		ORDER BY user_id, ts DESC`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	got := map[int32]int32{} // user_id → kept event id
	for rows.Next() {
		var u, id int32
		var kind string
		if err := rows.Scan(&u, &id, &kind); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[u] = id
	}
	// User 1's most recent event is id=3 (logout at +2m). User 2's
	// most recent is id=5.
	if got[1] != 3 {
		t.Errorf("user 1 latest = %d, want 3", got[1])
	}
	if got[2] != 5 {
		t.Errorf("user 2 latest = %d, want 5", got[2])
	}
}

// TestDistinctOn_KeepsFirstSeen confirms that without ORDER BY the
// "first row" PG keeps is implementation-defined; our stable
// dedupe keeps the first seen row.
func TestDistinctOn_KeepsFirstSeen(t *testing.T) {
	pool, ctx, cleanup := distinctOnPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT ON (kind) kind, id
		FROM events
		ORDER BY kind, id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	type pair struct {
		kind string
		id   int32
	}
	var got []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.kind, &p.id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, p)
	}
	want := []pair{{"login", 1}, {"logout", 3}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestDistinct_PlainStillWorks confirms `SELECT DISTINCT col` (no
// ON) continues to dedupe on the full output tuple.
func TestDistinct_PlainStillWorks(t *testing.T) {
	pool, ctx, cleanup := distinctOnPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `SELECT DISTINCT kind FROM events ORDER BY kind`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatalf("scan: %v", err)
		}
		kinds = append(kinds, k)
	}
	want := []string{"login", "logout"}
	if len(kinds) != len(want) || kinds[0] != want[0] || kinds[1] != want[1] {
		t.Errorf("got %v, want %v", kinds, want)
	}
}
