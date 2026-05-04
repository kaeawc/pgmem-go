package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func containsPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	if _, err := pool.Exec(ctx, `CREATE TABLE docs (id int PRIMARY KEY, body jsonb NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO docs (id, body) VALUES (1, $1), (2, $2), (3, $3)`,
		[]byte(`{"name":"alice","tags":["a","b"],"age":30}`),
		[]byte(`{"name":"bob","tags":["b","c"],"age":25}`),
		[]byte(`{"name":"carol","tags":[]}`),
	); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestJSONContains_ObjectKeyMatch(t *testing.T) {
	pool, ctx, cleanup := containsPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx,
		`SELECT id FROM docs WHERE body @> $1::jsonb ORDER BY id`,
		[]byte(`{"name":"alice"}`))
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
	if len(ids) != 1 || ids[0] != 1 {
		t.Errorf("got %v, want [1]", ids)
	}
}

func TestJSONContains_ArrayElementMatch(t *testing.T) {
	pool, ctx, cleanup := containsPool(t)
	defer cleanup()
	// Rows whose `tags` array contains "b".
	rows, err := pool.Query(ctx,
		`SELECT id FROM docs WHERE body -> 'tags' @> $1::jsonb ORDER BY id`,
		[]byte(`["b"]`))
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
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Errorf("got %v, want [1 2]", ids)
	}
}

func TestJSONContained_Reverse(t *testing.T) {
	pool, ctx, cleanup := containsPool(t)
	defer cleanup()
	var got bool
	if err := pool.QueryRow(ctx,
		`SELECT $1::jsonb <@ $2::jsonb`,
		[]byte(`{"name":"alice"}`),
		[]byte(`{"name":"alice","tags":["a","b"]}`),
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !got {
		t.Errorf("got false, want true")
	}
}

func TestJSONContains_Negative(t *testing.T) {
	pool, ctx, cleanup := containsPool(t)
	defer cleanup()
	var got bool
	if err := pool.QueryRow(ctx,
		`SELECT $1::jsonb @> $2::jsonb`,
		[]byte(`{"a":1}`),
		[]byte(`{"a":2}`),
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got {
		t.Errorf("got true, want false")
	}
}

func TestJSONContains_Nested(t *testing.T) {
	pool, ctx, cleanup := containsPool(t)
	defer cleanup()
	var got bool
	if err := pool.QueryRow(ctx,
		`SELECT $1::jsonb @> $2::jsonb`,
		[]byte(`{"a":{"b":1,"c":2}}`),
		[]byte(`{"a":{"b":1}}`),
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !got {
		t.Errorf("got false, want true")
	}
}
