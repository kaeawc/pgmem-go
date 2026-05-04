package postgres_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func floatPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	if _, err := pool.Exec(ctx, `CREATE TABLE measurements (id int PRIMARY KEY, ratio float8 NOT NULL, name text)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO measurements (id, ratio, name) VALUES (1, 0.5, 'half'), (2, 1.5, 'one-and-half'), (3, 3.14, 'pi')`,
	); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestFloat_Roundtrip(t *testing.T) {
	pool, ctx, cleanup := floatPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `SELECT id, ratio FROM measurements ORDER BY id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var got []float64
	for rows.Next() {
		var id int32
		var r float64
		if err := rows.Scan(&id, &r); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	want := []float64{0.5, 1.5, 3.14}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Errorf("[%d]: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestFloat_Comparison(t *testing.T) {
	pool, ctx, cleanup := floatPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `SELECT id FROM measurements WHERE ratio > 1.0 ORDER BY id`)
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
	if len(ids) != 2 || ids[0] != 2 || ids[1] != 3 {
		t.Errorf("got %v, want [2 3]", ids)
	}
}

func TestFloat_Arithmetic(t *testing.T) {
	pool, ctx, cleanup := floatPool(t)
	defer cleanup()
	var got float64
	if err := pool.QueryRow(ctx, `SELECT 0.1 + 0.2`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if math.Abs(got-0.3) > 1e-9 {
		t.Errorf("got %v, want ~0.3", got)
	}
}

func TestFloat_IntPlusFloat(t *testing.T) {
	pool, ctx, cleanup := floatPool(t)
	defer cleanup()
	var got float64
	if err := pool.QueryRow(ctx, `SELECT 1 + 0.5`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != 1.5 {
		t.Errorf("got %v, want 1.5", got)
	}
}

func TestFloat_DoublePrecisionAlias(t *testing.T) {
	pool, ctx, cleanup := floatPool(t)
	defer cleanup()
	var got float64
	if err := pool.QueryRow(ctx,
		`SELECT $1::double precision + 1.0`,
		float64(2.5),
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != 3.5 {
		t.Errorf("got %v, want 3.5", got)
	}
}

func TestFloat_DivByZero(t *testing.T) {
	pool, ctx, cleanup := floatPool(t)
	defer cleanup()
	var got float64
	if err := pool.QueryRow(ctx, `SELECT 1.0 / 0.0`).Scan(&got); err == nil {
		t.Errorf("expected error, got %v", got)
	}
}
