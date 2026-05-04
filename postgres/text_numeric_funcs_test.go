package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func textNumPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

func TestFormat_PlainString(t *testing.T) {
	pool, ctx, cleanup := textNumPool(t)
	defer cleanup()
	var s string
	if err := pool.QueryRow(ctx, `SELECT format('hello %s', 'world')`).Scan(&s); err != nil {
		t.Fatalf("query: %v", err)
	}
	if s != "hello world" {
		t.Errorf("got %q, want %q", s, "hello world")
	}
}

func TestFormat_PercentLiteral(t *testing.T) {
	pool, ctx, cleanup := textNumPool(t)
	defer cleanup()
	var s string
	if err := pool.QueryRow(ctx, `SELECT format('100%%')`).Scan(&s); err != nil {
		t.Fatalf("query: %v", err)
	}
	if s != "100%" {
		t.Errorf("got %q, want 100%%", s)
	}
}

func TestFormat_IdentifierAndLiteral(t *testing.T) {
	pool, ctx, cleanup := textNumPool(t)
	defer cleanup()
	var s string
	if err := pool.QueryRow(ctx, `SELECT format('SELECT * FROM %I WHERE x = %L', 'my table', 'a''b')`).Scan(&s); err != nil {
		t.Fatalf("query: %v", err)
	}
	want := `SELECT * FROM "my table" WHERE x = 'a''b'`
	if s != want {
		t.Errorf("got %q, want %q", s, want)
	}
}

func TestFormat_NullSAsEmpty(t *testing.T) {
	pool, ctx, cleanup := textNumPool(t)
	defer cleanup()
	var s string
	if err := pool.QueryRow(ctx, `SELECT format('[%s]', NULL)`).Scan(&s); err != nil {
		t.Fatalf("query: %v", err)
	}
	if s != "[]" {
		t.Errorf("got %q, want []", s)
	}
}

func TestFormat_NullLAsNULL(t *testing.T) {
	pool, ctx, cleanup := textNumPool(t)
	defer cleanup()
	var s string
	if err := pool.QueryRow(ctx, `SELECT format('value: %L', NULL)`).Scan(&s); err != nil {
		t.Fatalf("query: %v", err)
	}
	if s != "value: NULL" {
		t.Errorf("got %q, want %q", s, "value: NULL")
	}
}

func TestChr(t *testing.T) {
	pool, ctx, cleanup := textNumPool(t)
	defer cleanup()
	var s string
	if err := pool.QueryRow(ctx, `SELECT chr(65)`).Scan(&s); err != nil {
		t.Fatalf("query: %v", err)
	}
	if s != "A" {
		t.Errorf("got %q, want A", s)
	}
}

func TestChr_RejectsZero(t *testing.T) {
	pool, ctx, cleanup := textNumPool(t)
	defer cleanup()
	var s string
	if err := pool.QueryRow(ctx, `SELECT chr(0)`).Scan(&s); err == nil {
		t.Fatalf("expected error for chr(0)")
	}
}

func TestAscii(t *testing.T) {
	pool, ctx, cleanup := textNumPool(t)
	defer cleanup()
	var n int32
	if err := pool.QueryRow(ctx, `SELECT ascii('Apple')`).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 65 {
		t.Errorf("got %d, want 65", n)
	}
}

func TestAscii_Empty(t *testing.T) {
	pool, ctx, cleanup := textNumPool(t)
	defer cleanup()
	var n int32
	if err := pool.QueryRow(ctx, `SELECT ascii('')`).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 0 {
		t.Errorf("got %d, want 0", n)
	}
}

func TestToHex(t *testing.T) {
	pool, ctx, cleanup := textNumPool(t)
	defer cleanup()
	var s string
	if err := pool.QueryRow(ctx, `SELECT to_hex(255)`).Scan(&s); err != nil {
		t.Fatalf("query: %v", err)
	}
	if s != "ff" {
		t.Errorf("got %q, want ff", s)
	}
}

func TestMD5(t *testing.T) {
	pool, ctx, cleanup := textNumPool(t)
	defer cleanup()
	var s string
	if err := pool.QueryRow(ctx, `SELECT md5('hello')`).Scan(&s); err != nil {
		t.Fatalf("query: %v", err)
	}
	if s != "5d41402abc4b2a76b9719d911017c592" {
		t.Errorf("got %q", s)
	}
	if !strings.HasPrefix(s, "5d41") {
		t.Errorf("md5 prefix mismatch: %q", s)
	}
}

func TestSHA256_Bytea(t *testing.T) {
	pool, ctx, cleanup := textNumPool(t)
	defer cleanup()
	var b []byte
	if err := pool.QueryRow(ctx, `SELECT sha256('hello'::bytea)`).Scan(&b); err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(b) != 32 {
		t.Errorf("sha256 returned %d bytes, want 32", len(b))
	}
}

func TestSHA512_Bytea(t *testing.T) {
	pool, ctx, cleanup := textNumPool(t)
	defer cleanup()
	var b []byte
	if err := pool.QueryRow(ctx, `SELECT sha512('hello'::bytea)`).Scan(&b); err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(b) != 64 {
		t.Errorf("sha512 returned %d bytes, want 64", len(b))
	}
}
