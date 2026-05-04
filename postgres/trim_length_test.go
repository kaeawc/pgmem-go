package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func tlPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

func TestLtrim_Default(t *testing.T) {
	pool, ctx, cleanup := tlPool(t)
	defer cleanup()
	var got string
	if err := pool.QueryRow(ctx, `SELECT ltrim('   hello')`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestRtrim_Default(t *testing.T) {
	pool, ctx, cleanup := tlPool(t)
	defer cleanup()
	var got string
	if err := pool.QueryRow(ctx, `SELECT rtrim('hello   ')`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestBtrim_Cutset(t *testing.T) {
	pool, ctx, cleanup := tlPool(t)
	defer cleanup()
	var got string
	if err := pool.QueryRow(ctx, `SELECT btrim('xxhelloxxx', 'x')`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestLtrim_Cutset(t *testing.T) {
	pool, ctx, cleanup := tlPool(t)
	defer cleanup()
	var got string
	if err := pool.QueryRow(ctx, `SELECT ltrim('//path/x', '/')`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != "path/x" {
		t.Errorf("got %q, want %q", got, "path/x")
	}
}

func TestCharLength(t *testing.T) {
	pool, ctx, cleanup := tlPool(t)
	defer cleanup()
	var got int32
	// Two characters that take 2 bytes each in UTF-8.
	if err := pool.QueryRow(ctx, `SELECT char_length('héllo')`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != 5 {
		t.Errorf("got %d, want 5", got)
	}
}

func TestOctetLength_Text(t *testing.T) {
	pool, ctx, cleanup := tlPool(t)
	defer cleanup()
	var got int32
	if err := pool.QueryRow(ctx, `SELECT octet_length('héllo')`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != 6 { // h(1) + é(2) + l(1) + l(1) + o(1)
		t.Errorf("got %d, want 6", got)
	}
}

func TestOctetLength_Bytea(t *testing.T) {
	pool, ctx, cleanup := tlPool(t)
	defer cleanup()
	var got int32
	if err := pool.QueryRow(ctx, `SELECT octet_length($1::bytea)`, []byte{0, 1, 2, 3}).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != 4 {
		t.Errorf("got %d, want 4", got)
	}
}

func TestTrim_NullPropagates(t *testing.T) {
	pool, ctx, cleanup := tlPool(t)
	defer cleanup()
	var got *string
	if err := pool.QueryRow(ctx, `SELECT ltrim(NULL::text)`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != nil {
		t.Errorf("got %q, want NULL", *got)
	}
}
