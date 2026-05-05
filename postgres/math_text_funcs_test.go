package postgres_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func mathTxtPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

func TestFloor(t *testing.T) {
	pool, ctx, cleanup := mathTxtPool(t)
	defer cleanup()
	var got float64
	if err := pool.QueryRow(ctx, `SELECT floor(3.7)`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != 3 {
		t.Errorf("got %v, want 3", got)
	}
}

func TestCeil(t *testing.T) {
	pool, ctx, cleanup := mathTxtPool(t)
	defer cleanup()
	var got float64
	if err := pool.QueryRow(ctx, `SELECT ceil(3.2)`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != 4 {
		t.Errorf("got %v, want 4", got)
	}
}

func TestRound_Default(t *testing.T) {
	pool, ctx, cleanup := mathTxtPool(t)
	defer cleanup()
	var got float64
	if err := pool.QueryRow(ctx, `SELECT round(3.5)`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != 4 {
		t.Errorf("got %v, want 4", got)
	}
}

func TestRound_Scale(t *testing.T) {
	pool, ctx, cleanup := mathTxtPool(t)
	defer cleanup()
	var got float64
	if err := pool.QueryRow(ctx, `SELECT round(3.14159, 2)`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if math.Abs(got-3.14) > 1e-9 {
		t.Errorf("got %v, want 3.14", got)
	}
}

func TestPower(t *testing.T) {
	pool, ctx, cleanup := mathTxtPool(t)
	defer cleanup()
	var got float64
	if err := pool.QueryRow(ctx, `SELECT power(2, 10)`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != 1024 {
		t.Errorf("got %v, want 1024", got)
	}
}

func TestSqrt(t *testing.T) {
	pool, ctx, cleanup := mathTxtPool(t)
	defer cleanup()
	var got float64
	if err := pool.QueryRow(ctx, `SELECT sqrt(81)`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != 9 {
		t.Errorf("got %v, want 9", got)
	}
}

func TestSign(t *testing.T) {
	pool, ctx, cleanup := mathTxtPool(t)
	defer cleanup()
	cases := []struct {
		in   string
		want float64
	}{
		{"5", 1}, {"-3", -1}, {"0", 0},
	}
	for _, c := range cases {
		var got float64
		if err := pool.QueryRow(ctx, `SELECT sign(`+c.in+`)`).Scan(&got); err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("sign(%s): got %v, want %v", c.in, got, c.want)
		}
	}
}

func TestRandom(t *testing.T) {
	pool, ctx, cleanup := mathTxtPool(t)
	defer cleanup()
	var got float64
	if err := pool.QueryRow(ctx, `SELECT random()`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got < 0 || got >= 1 {
		t.Errorf("got %v, want in [0, 1)", got)
	}
}

func TestPi(t *testing.T) {
	pool, ctx, cleanup := mathTxtPool(t)
	defer cleanup()
	var got float64
	if err := pool.QueryRow(ctx, `SELECT pi()`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if math.Abs(got-math.Pi) > 1e-9 {
		t.Errorf("got %v, want pi", got)
	}
}

func TestLpad_Default(t *testing.T) {
	pool, ctx, cleanup := mathTxtPool(t)
	defer cleanup()
	var got string
	if err := pool.QueryRow(ctx, `SELECT lpad('42', 5)`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != "   42" {
		t.Errorf("got %q, want %q", got, "   42")
	}
}

func TestLpad_CustomFill(t *testing.T) {
	pool, ctx, cleanup := mathTxtPool(t)
	defer cleanup()
	var got string
	if err := pool.QueryRow(ctx, `SELECT lpad('42', 6, 'ab')`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != "abab42" {
		t.Errorf("got %q, want %q", got, "abab42")
	}
}

func TestRpad(t *testing.T) {
	pool, ctx, cleanup := mathTxtPool(t)
	defer cleanup()
	var got string
	if err := pool.QueryRow(ctx, `SELECT rpad('42', 5, '0')`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != "42000" {
		t.Errorf("got %q, want %q", got, "42000")
	}
}

func TestLpad_Truncates(t *testing.T) {
	pool, ctx, cleanup := mathTxtPool(t)
	defer cleanup()
	var got string
	if err := pool.QueryRow(ctx, `SELECT lpad('hello', 3)`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != "hel" {
		t.Errorf("got %q, want %q", got, "hel")
	}
}
