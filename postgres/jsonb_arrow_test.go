package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func arrowPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
		`INSERT INTO docs (id, body) VALUES (1, $1), (2, $2)`,
		[]byte(`{"name":"alice","age":30,"tags":["a","b","c"]}`),
		[]byte(`{"name":"bob","age":25,"tags":[]}`),
	); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestJSONArrow_TextKey(t *testing.T) {
	pool, ctx, cleanup := arrowPool(t)
	defer cleanup()
	var got string
	if err := pool.QueryRow(ctx,
		`SELECT body ->> 'name' FROM docs WHERE id = 1`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != "alice" {
		t.Errorf("got %q, want %q", got, "alice")
	}
}

func TestJSONArrow_JSONKey(t *testing.T) {
	pool, ctx, cleanup := arrowPool(t)
	defer cleanup()
	var got []byte
	if err := pool.QueryRow(ctx,
		`SELECT body -> 'tags' FROM docs WHERE id = 1`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if string(got) != `["a","b","c"]` {
		t.Errorf("got %s, want %s", got, `["a","b","c"]`)
	}
}

func TestJSONArrow_FilterByText(t *testing.T) {
	pool, ctx, cleanup := arrowPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx,
		`SELECT id FROM docs WHERE body ->> 'name' = 'alice'`)
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

func TestJSONArrow_ArrayIndex(t *testing.T) {
	pool, ctx, cleanup := arrowPool(t)
	defer cleanup()
	var got string
	if err := pool.QueryRow(ctx,
		`SELECT body -> 'tags' ->> 1 FROM docs WHERE id = 1`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != "b" {
		t.Errorf("got %q, want %q", got, "b")
	}
}

func TestJSONArrow_MissingKeyIsNull(t *testing.T) {
	pool, ctx, cleanup := arrowPool(t)
	defer cleanup()
	var got *string
	if err := pool.QueryRow(ctx,
		`SELECT body ->> 'missing' FROM docs WHERE id = 1`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != nil {
		t.Errorf("got %q, want NULL", *got)
	}
}

func TestJSONArrow_OutOfRangeIsNull(t *testing.T) {
	pool, ctx, cleanup := arrowPool(t)
	defer cleanup()
	var got *string
	if err := pool.QueryRow(ctx,
		`SELECT body -> 'tags' ->> 99 FROM docs WHERE id = 1`,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != nil {
		t.Errorf("got %q, want NULL", *got)
	}
}
