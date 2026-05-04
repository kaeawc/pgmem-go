package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func arrStrPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

func TestArrayLength_NonEmpty(t *testing.T) {
	pool, ctx, cleanup := arrStrPool(t)
	defer cleanup()
	var n *int32
	if err := pool.QueryRow(ctx, `SELECT array_length($1::int[], 1)`, []int32{1, 2, 3}).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n == nil || *n != 3 {
		t.Errorf("got %v, want 3", n)
	}
}

func TestArrayLength_Empty(t *testing.T) {
	pool, ctx, cleanup := arrStrPool(t)
	defer cleanup()
	var n *int32
	if err := pool.QueryRow(ctx, `SELECT array_length($1::int[], 1)`, []int32{}).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != nil {
		t.Errorf("empty array_length should be NULL, got %v", *n)
	}
}

func TestCardinality(t *testing.T) {
	pool, ctx, cleanup := arrStrPool(t)
	defer cleanup()
	var n int32
	if err := pool.QueryRow(ctx, `SELECT cardinality($1::text[])`, []string{"a", "b", "c"}).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 3 {
		t.Errorf("got %d, want 3", n)
	}
	// Cardinality of empty array is 0 (unlike array_length).
	if err := pool.QueryRow(ctx, `SELECT cardinality($1::text[])`, []string{}).Scan(&n); err != nil {
		t.Fatalf("empty query: %v", err)
	}
	if n != 0 {
		t.Errorf("empty cardinality got %d, want 0", n)
	}
}

func TestArrayToString_PlainSep(t *testing.T) {
	pool, ctx, cleanup := arrStrPool(t)
	defer cleanup()
	var s string
	if err := pool.QueryRow(ctx, `SELECT array_to_string($1::text[], ',')`, []string{"a", "b", "c"}).Scan(&s); err != nil {
		t.Fatalf("query: %v", err)
	}
	if s != "a,b,c" {
		t.Errorf("got %q, want a,b,c", s)
	}
}

func TestArrayToString_IntArray(t *testing.T) {
	pool, ctx, cleanup := arrStrPool(t)
	defer cleanup()
	var s string
	if err := pool.QueryRow(ctx, `SELECT array_to_string($1::int[], '-')`, []int32{1, 2, 3}).Scan(&s); err != nil {
		t.Fatalf("query: %v", err)
	}
	if s != "1-2-3" {
		t.Errorf("got %q, want 1-2-3", s)
	}
}

func TestStringToArray_Basic(t *testing.T) {
	pool, ctx, cleanup := arrStrPool(t)
	defer cleanup()
	var a []string
	if err := pool.QueryRow(ctx, `SELECT string_to_array('a,b,c', ',')`).Scan(&a); err != nil {
		t.Fatalf("query: %v", err)
	}
	want := []string{"a", "b", "c"}
	if len(a) != len(want) {
		t.Fatalf("got %v, want %v", a, want)
	}
	for i := range want {
		if a[i] != want[i] {
			t.Errorf("[%d] got %q, want %q", i, a[i], want[i])
		}
	}
}

func TestSplitPart_Positive(t *testing.T) {
	pool, ctx, cleanup := arrStrPool(t)
	defer cleanup()
	var s string
	if err := pool.QueryRow(ctx, `SELECT split_part('a,b,c', ',', 2)`).Scan(&s); err != nil {
		t.Fatalf("query: %v", err)
	}
	if s != "b" {
		t.Errorf("got %q, want b", s)
	}
}

func TestSplitPart_Negative(t *testing.T) {
	pool, ctx, cleanup := arrStrPool(t)
	defer cleanup()
	var s string
	if err := pool.QueryRow(ctx, `SELECT split_part('a,b,c', ',', -1)`).Scan(&s); err != nil {
		t.Fatalf("query: %v", err)
	}
	if s != "c" {
		t.Errorf("got %q, want c", s)
	}
}

func TestSplitPart_OutOfRange(t *testing.T) {
	pool, ctx, cleanup := arrStrPool(t)
	defer cleanup()
	var s string
	if err := pool.QueryRow(ctx, `SELECT split_part('a,b', ',', 5)`).Scan(&s); err != nil {
		t.Fatalf("query: %v", err)
	}
	if s != "" {
		t.Errorf("got %q, want empty", s)
	}
}

func TestRepeat(t *testing.T) {
	pool, ctx, cleanup := arrStrPool(t)
	defer cleanup()
	var s string
	if err := pool.QueryRow(ctx, `SELECT repeat('ab', 3)`).Scan(&s); err != nil {
		t.Fatalf("query: %v", err)
	}
	if s != "ababab" {
		t.Errorf("got %q, want ababab", s)
	}
	if err := pool.QueryRow(ctx, `SELECT repeat('ab', 0)`).Scan(&s); err != nil {
		t.Fatalf("query: %v", err)
	}
	if s != "" {
		t.Errorf("repeat 0 got %q, want empty", s)
	}
}

func TestReverse(t *testing.T) {
	pool, ctx, cleanup := arrStrPool(t)
	defer cleanup()
	var s string
	if err := pool.QueryRow(ctx, `SELECT reverse('hello')`).Scan(&s); err != nil {
		t.Fatalf("query: %v", err)
	}
	if s != "olleh" {
		t.Errorf("got %q, want olleh", s)
	}
}
