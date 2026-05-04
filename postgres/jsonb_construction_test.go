package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func jsonbCtorPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestToJSONB_Number(t *testing.T) {
	pool, ctx, cleanup := jsonbCtorPool(t)
	defer cleanup()
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT to_jsonb(42)`).Scan(&raw); err != nil {
		t.Fatalf("query: %v", err)
	}
	if string(raw) != "42" {
		t.Errorf("got %q, want 42", raw)
	}
}

func TestToJSONB_String(t *testing.T) {
	pool, ctx, cleanup := jsonbCtorPool(t)
	defer cleanup()
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT to_jsonb('hello')`).Scan(&raw); err != nil {
		t.Fatalf("query: %v", err)
	}
	if string(raw) != `"hello"` {
		t.Errorf("got %q, want \"hello\"", raw)
	}
}

func TestJSONBBuildObject_Mixed(t *testing.T) {
	pool, ctx, cleanup := jsonbCtorPool(t)
	defer cleanup()
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT jsonb_build_object('name', 'alice', 'age', 30, 'active', true)`).Scan(&raw); err != nil {
		t.Fatalf("query: %v", err)
	}
	want := `{"name":"alice","age":30,"active":true}`
	if string(raw) != want {
		t.Errorf("got %q, want %q", raw, want)
	}
}

func TestJSONBBuildObject_OddArgsFails(t *testing.T) {
	pool, ctx, cleanup := jsonbCtorPool(t)
	defer cleanup()
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT jsonb_build_object('k')`).Scan(&raw); err == nil {
		t.Fatalf("expected error for odd-length args")
	}
}

func TestJSONBBuildObject_NullKeyFails(t *testing.T) {
	pool, ctx, cleanup := jsonbCtorPool(t)
	defer cleanup()
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT jsonb_build_object(NULL, 'v')`).Scan(&raw); err == nil {
		t.Fatalf("expected error for null key")
	}
}

func TestJSONBBuildArray_Mixed(t *testing.T) {
	pool, ctx, cleanup := jsonbCtorPool(t)
	defer cleanup()
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT jsonb_build_array(1, 'two', true, NULL)`).Scan(&raw); err != nil {
		t.Fatalf("query: %v", err)
	}
	want := `[1,"two",true,null]`
	if string(raw) != want {
		t.Errorf("got %q, want %q", raw, want)
	}
}

func TestJSONBTypeof(t *testing.T) {
	pool, ctx, cleanup := jsonbCtorPool(t)
	defer cleanup()
	cases := []struct {
		expr string
		want string
	}{
		{`jsonb_typeof('{"a":1}'::jsonb)`, "object"},
		{`jsonb_typeof('[1,2]'::jsonb)`, "array"},
		{`jsonb_typeof('"hi"'::jsonb)`, "string"},
		{`jsonb_typeof('42'::jsonb)`, "number"},
		{`jsonb_typeof('true'::jsonb)`, "boolean"},
		{`jsonb_typeof('null'::jsonb)`, "null"},
	}
	for _, c := range cases {
		var got string
		if err := pool.QueryRow(ctx, "SELECT "+c.expr).Scan(&got); err != nil {
			t.Errorf("%s: %v", c.expr, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.expr, got, c.want)
		}
	}
}

func TestJSONBArrayLength(t *testing.T) {
	pool, ctx, cleanup := jsonbCtorPool(t)
	defer cleanup()
	var n int32
	if err := pool.QueryRow(ctx, `SELECT jsonb_array_length('[1, 2, 3, 4]'::jsonb)`).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 4 {
		t.Errorf("got %d, want 4", n)
	}
}

func TestJSONBArrayLength_NonArrayErrors(t *testing.T) {
	pool, ctx, cleanup := jsonbCtorPool(t)
	defer cleanup()
	var n int32
	if err := pool.QueryRow(ctx, `SELECT jsonb_array_length('{"a":1}'::jsonb)`).Scan(&n); err == nil {
		t.Fatalf("expected error for non-array input")
	}
}

func TestJSONBBuildObject_NestedJSONB(t *testing.T) {
	pool, ctx, cleanup := jsonbCtorPool(t)
	defer cleanup()
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT jsonb_build_object('inner', jsonb_build_object('x', 1))`).Scan(&raw); err != nil {
		t.Fatalf("query: %v", err)
	}
	want := `{"inner":{"x":1}}`
	if string(raw) != want {
		t.Errorf("got %q, want %q", raw, want)
	}
}
