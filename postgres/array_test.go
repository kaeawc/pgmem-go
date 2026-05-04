package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func arrayPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	if _, err := pool.Exec(ctx, `CREATE TABLE users (id bigint PRIMARY KEY, name text NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, name) VALUES (1, 'alice'), (2, 'bob'), (3, 'carol'), (4, 'dan')`,
	); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestArray_AnyBigint(t *testing.T) {
	pool, ctx, cleanup := arrayPool(t)
	defer cleanup()
	ids := []int64{1, 3}
	rows, err := pool.Query(ctx,
		`SELECT id, name FROM users WHERE id = ANY($1::bigint[]) ORDER BY id`,
		ids)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	type pair struct {
		id   int64
		name string
	}
	var got []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.id, &p.name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, p)
	}
	want := []pair{{1, "alice"}, {3, "carol"}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestArray_AnyText(t *testing.T) {
	pool, ctx, cleanup := arrayPool(t)
	defer cleanup()
	names := []string{"bob", "dan"}
	rows, err := pool.Query(ctx,
		`SELECT id FROM users WHERE name = ANY($1::text[]) ORDER BY id`,
		names)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 2 || ids[0] != 2 || ids[1] != 4 {
		t.Errorf("got %v, want [2 4]", ids)
	}
}

func TestArray_EmptyArrayMatchesNothing(t *testing.T) {
	pool, ctx, cleanup := arrayPool(t)
	defer cleanup()
	var got int64
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM users WHERE id = ANY($1::bigint[])`,
		[]int64{},
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

func TestArray_BigintLiteral(t *testing.T) {
	pool, ctx, cleanup := arrayPool(t)
	defer cleanup()
	// The cast forces our parser to accept the array type name; the
	// {…} text literal exercises DecodeText.
	rows, err := pool.Query(ctx,
		`SELECT id FROM users WHERE id = ANY($1::bigint[]) ORDER BY id`,
		[]int64{2, 4})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 2 {
		t.Errorf("got %d ids, want 2", len(ids))
	}
}
