package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func regexpPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

func TestRegexpReplace_FirstMatchByDefault(t *testing.T) {
	pool, ctx, cleanup := regexpPool(t)
	defer cleanup()
	var s string
	if err := pool.QueryRow(ctx, `SELECT regexp_replace('foo bar foo', 'foo', 'BAZ')`).Scan(&s); err != nil {
		t.Fatalf("query: %v", err)
	}
	if s != "BAZ bar foo" {
		t.Errorf("got %q, want %q", s, "BAZ bar foo")
	}
}

func TestRegexpReplace_GlobalFlag(t *testing.T) {
	pool, ctx, cleanup := regexpPool(t)
	defer cleanup()
	var s string
	if err := pool.QueryRow(ctx, `SELECT regexp_replace('foo bar foo', 'foo', 'BAZ', 'g')`).Scan(&s); err != nil {
		t.Fatalf("query: %v", err)
	}
	if s != "BAZ bar BAZ" {
		t.Errorf("got %q, want %q", s, "BAZ bar BAZ")
	}
}

func TestRegexpReplace_CaseInsensitive(t *testing.T) {
	pool, ctx, cleanup := regexpPool(t)
	defer cleanup()
	var s string
	if err := pool.QueryRow(ctx, `SELECT regexp_replace('Hello WORLD', 'world', 'there', 'gi')`).Scan(&s); err != nil {
		t.Fatalf("query: %v", err)
	}
	if s != "Hello there" {
		t.Errorf("got %q, want %q", s, "Hello there")
	}
}

func TestRegexpReplace_BackReference(t *testing.T) {
	pool, ctx, cleanup := regexpPool(t)
	defer cleanup()
	var s string
	if err := pool.QueryRow(ctx, `SELECT regexp_replace('John Smith', '(\w+) (\w+)', '$2 $1')`).Scan(&s); err != nil {
		t.Fatalf("query: %v", err)
	}
	if s != "Smith John" {
		t.Errorf("got %q, want %q", s, "Smith John")
	}
}

func TestRegexpMatch_Groups(t *testing.T) {
	pool, ctx, cleanup := regexpPool(t)
	defer cleanup()
	var got []string
	if err := pool.QueryRow(ctx, `SELECT regexp_match('foo123bar', '([a-z]+)(\d+)')`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	want := []string{"foo", "123"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRegexpMatch_NoGroupReturnsWholeMatch(t *testing.T) {
	pool, ctx, cleanup := regexpPool(t)
	defer cleanup()
	var got []string
	if err := pool.QueryRow(ctx, `SELECT regexp_match('hello world', 'world')`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 || got[0] != "world" {
		t.Errorf("got %v, want [world]", got)
	}
}

func TestRegexpMatch_NoMatchReturnsNull(t *testing.T) {
	pool, ctx, cleanup := regexpPool(t)
	defer cleanup()
	var got []string
	if err := pool.QueryRow(ctx, `SELECT regexp_match('hello', 'xyz')`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestRegexpSplitToArray(t *testing.T) {
	pool, ctx, cleanup := regexpPool(t)
	defer cleanup()
	var got []string
	if err := pool.QueryRow(ctx, `SELECT regexp_split_to_array('a,,b;c', '[,;]')`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	want := []string{"a", "", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRegexpReplace_BadPattern(t *testing.T) {
	pool, ctx, cleanup := regexpPool(t)
	defer cleanup()
	var s string
	if err := pool.QueryRow(ctx, `SELECT regexp_replace('x', '(', 'y')`).Scan(&s); err == nil {
		t.Fatalf("expected error for bad regex pattern")
	}
}
