package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func aaPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, kind text NOT NULL, n int NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO t (id, kind, n) VALUES (1, 'a', 10), (2, 'a', 20), (3, 'b', 30), (4, 'b', 40)`,
	); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestArrayAgg_Int(t *testing.T) {
	pool, ctx, cleanup := aaPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx,
		`SELECT kind, array_agg(n) FROM t GROUP BY kind ORDER BY kind`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	type pair struct {
		kind string
		ns   []int32
	}
	var got []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.kind, &p.ns); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, p)
	}
	if len(got) != 2 {
		t.Fatalf("got %d groups, want 2", len(got))
	}
	if got[0].kind != "a" || len(got[0].ns) != 2 || got[0].ns[0] != 10 || got[0].ns[1] != 20 {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].kind != "b" || len(got[1].ns) != 2 || got[1].ns[0] != 30 || got[1].ns[1] != 40 {
		t.Errorf("got[1] = %+v", got[1])
	}
}

func TestArrayAgg_Text(t *testing.T) {
	pool, ctx, cleanup := aaPool(t)
	defer cleanup()
	var got []string
	if err := pool.QueryRow(ctx,
		`SELECT array_agg(kind) FROM t WHERE n < 30`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "a" {
		t.Errorf("got %v", got)
	}
}

func TestUnnest_Int(t *testing.T) {
	pool, ctx, cleanup := aaPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx,
		`SELECT id FROM t WHERE id IN (SELECT * FROM unnest($1::bigint[])) ORDER BY id`,
		[]int64{2, 4})
	if err != nil {
		t.Fatalf("query: %v", err)
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
	if len(got) != 2 || got[0] != 2 || got[1] != 4 {
		t.Errorf("got %v", got)
	}
}

func TestUnnest_Text(t *testing.T) {
	pool, ctx, cleanup := aaPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx,
		`SELECT u FROM unnest($1::text[]) AS u ORDER BY u`,
		[]string{"banana", "apple", "cherry"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, s)
	}
	want := []string{"apple", "banana", "cherry"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

// Round-trip: array_agg into unnest, and verify the elements.
func TestArrayAggThenUnnest(t *testing.T) {
	pool, ctx, cleanup := aaPool(t)
	defer cleanup()
	var got int32
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM unnest((SELECT array_agg(n) FROM t)::bigint[])`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != 4 {
		t.Errorf("got %d, want 4", got)
	}
}
