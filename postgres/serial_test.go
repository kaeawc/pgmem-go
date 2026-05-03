package postgres_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

// TestSerial_OverWire is the sqlc-style happy path: a SERIAL primary
// key is auto-filled by the engine, returned via RETURNING, and
// successive inserts get sequential ids.
func TestSerial_OverWire(t *testing.T) {
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

	if _, err := pool.Exec(ctx, `CREATE TABLE notes (id SERIAL PRIMARY KEY, body text NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}

	want := []int32{1, 2, 3}
	for i, body := range []string{"alpha", "beta", "gamma"} {
		var id int32
		if err := pool.QueryRow(ctx,
			`INSERT INTO notes (body) VALUES ($1) RETURNING id`,
			body,
		).Scan(&id); err != nil {
			t.Fatalf("INSERT %d: %v", i, err)
		}
		if id != want[i] {
			t.Errorf("insert %d returned id %d, want %d", i, id, want[i])
		}
	}

	// Read back; sort by id for determinism.
	rows, err := pool.Query(ctx, `SELECT id FROM notes`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	var got []int32
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		got = append(got, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Errorf("ids: got %v, want [1 2 3]", got)
	}
}

// TestBigSerial_OverWire confirms BIGSERIAL through the wire returns
// int8 and pgx scans it into int64.
func TestBigSerial_OverWire(t *testing.T) {
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

	if _, err := pool.Exec(ctx, `CREATE TABLE big (id BIGSERIAL PRIMARY KEY, label text NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	var id int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO big (label) VALUES ($1) RETURNING id`, "x",
	).Scan(&id); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if id != 1 {
		t.Errorf("id: got %d, want 1", id)
	}
}
