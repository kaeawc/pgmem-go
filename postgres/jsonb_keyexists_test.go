package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func keyExistsPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
		[]byte(`{"name":"alice","tags":["a","b"]}`),
		[]byte(`{"name":"bob","age":25}`),
		[]byte(`["x","y","z"]`),
	); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestJSONKeyExists_Object(t *testing.T) {
	pool, ctx, cleanup := keyExistsPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx,
		`SELECT id FROM docs WHERE body ? 'age' ORDER BY id`)
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
	if len(ids) != 1 || ids[0] != 2 {
		t.Errorf("got %v, want [2]", ids)
	}
}

func TestJSONKeyExists_ArrayElement(t *testing.T) {
	pool, ctx, cleanup := keyExistsPool(t)
	defer cleanup()
	var got bool
	if err := pool.QueryRow(ctx,
		`SELECT $1::jsonb ? 'y'`, []byte(`["x","y","z"]`),
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !got {
		t.Errorf("got false, want true")
	}
}

func TestJSONKeyExists_NotPresent(t *testing.T) {
	pool, ctx, cleanup := keyExistsPool(t)
	defer cleanup()
	var got bool
	if err := pool.QueryRow(ctx,
		`SELECT $1::jsonb ? 'missing'`, []byte(`{"a":1}`),
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got {
		t.Errorf("got true, want false")
	}
}

func TestJSONKeyExists_NestedKeyNotChecked(t *testing.T) {
	pool, ctx, cleanup := keyExistsPool(t)
	defer cleanup()
	// `?` is top-level only — `b` lives nested under `a`, so it's
	// reported as absent.
	var got bool
	if err := pool.QueryRow(ctx,
		`SELECT $1::jsonb ? 'b'`, []byte(`{"a":{"b":1}}`),
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got {
		t.Errorf("got true, want false (nested keys not visible)")
	}
}
