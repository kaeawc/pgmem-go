package postgres_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func distinctPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	if _, err := pool.Exec(ctx, `CREATE TABLE events (id int PRIMARY KEY, region text NOT NULL, kind text NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO events (id, region, kind) VALUES
		(1, 'east', 'view'),
		(2, 'east', 'view'),
		(3, 'east', 'click'),
		(4, 'west', 'view'),
		(5, 'west', 'view')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestDistinct_SingleColumn(t *testing.T) {
	pool, ctx, cleanup := distinctPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `SELECT DISTINCT region FROM events`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	var got []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		got = append(got, s)
	}
	sort.Strings(got)
	want := []string{"east", "west"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDistinct_MultiColumn(t *testing.T) {
	pool, ctx, cleanup := distinctPool(t)
	defer cleanup()
	type pair struct{ region, kind string }
	rows, err := pool.Query(ctx, `SELECT DISTINCT region, kind FROM events`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	var got []pair
	for rows.Next() {
		var p pair
		_ = rows.Scan(&p.region, &p.kind)
		got = append(got, p)
	}
	sort.Slice(got, func(i, j int) bool {
		if got[i].region != got[j].region {
			return got[i].region < got[j].region
		}
		return got[i].kind < got[j].kind
	})
	want := []pair{
		{"east", "click"},
		{"east", "view"},
		{"west", "view"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("row %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestDistinct_OnEmpty(t *testing.T) {
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
	if _, err := pool.Exec(ctx, `CREATE TABLE empty (n int)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	rows, err := pool.Query(ctx, `SELECT DISTINCT n FROM empty`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	count := 0
	for rows.Next() {
		count++
	}
	if count != 0 {
		t.Errorf("rows: got %d, want 0", count)
	}
}

// TestDistinct_ComposesWithOrder confirms DISTINCT + ORDER BY play
// nicely. ORDER BY sees the DISTINCT output (post-dedup), so
// `ORDER BY region` after `SELECT DISTINCT region` works.
func TestDistinct_ComposesWithOrder(t *testing.T) {
	pool, ctx, cleanup := distinctPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `SELECT DISTINCT region FROM events ORDER BY region DESC`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	var got []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		got = append(got, s)
	}
	want := []string{"west", "east"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v, want %v", got, want)
	}
}
