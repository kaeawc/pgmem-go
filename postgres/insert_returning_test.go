package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

// TestInsertReturning_OverWire is the sqlc happy-path: a single-row
// INSERT ... RETURNING id picks up the value via QueryRow().Scan,
// exactly as a generated `CreateUser` would.
func TestInsertReturning_OverWire(t *testing.T) {
	srv, err := postgres.Start(t)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, srv.DSN())
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, `CREATE TABLE accounts (id int PRIMARY KEY, name text NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}

	var id int32
	if err := pool.QueryRow(ctx,
		`INSERT INTO accounts (id, name) VALUES ($1, $2) RETURNING id`,
		int32(42), "alice",
	).Scan(&id); err != nil {
		t.Fatalf("INSERT RETURNING: %v", err)
	}
	if id != 42 {
		t.Errorf("returned id: got %d, want 42", id)
	}
}

// TestInsertReturning_MultiRow exercises the multi-row path. pgx's
// Query (vs QueryRow) should yield one row per inserted tuple, in
// insertion order.
func TestInsertReturning_MultiRow(t *testing.T) {
	srv, err := postgres.Start(t)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, srv.DSN())
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, `CREATE TABLE counters (n int NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}

	rows, err := pool.Query(ctx,
		`INSERT INTO counters (n) VALUES ($1), ($2), ($3) RETURNING n`,
		int32(10), int32(20), int32(30),
	)
	if err != nil {
		t.Fatalf("INSERT RETURNING: %v", err)
	}
	var got []int32
	for rows.Next() {
		var n int32
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		got = append(got, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	want := []int32{10, 20, 30}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("row %d: got %d, want %d", i, got[i], want[i])
		}
	}
}
