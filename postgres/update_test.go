package postgres_test

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

// TestUpdate_OverWire is the wire-level happy path for UPDATE.
func TestUpdate_OverWire(t *testing.T) {
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
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name) VALUES (1, 'alice'), (2, 'bob')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	tag, err := pool.Exec(ctx, `UPDATE accounts SET name = $1 WHERE id = $2`, "ALICE", int32(1))
	if err != nil {
		t.Fatalf("UPDATE: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Errorf("RowsAffected: got %d, want 1", tag.RowsAffected())
	}

	var got string
	if err := pool.QueryRow(ctx, `SELECT name FROM accounts WHERE id = $1`, int32(1)).Scan(&got); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if got != "ALICE" {
		t.Errorf("name: got %q, want %q", got, "ALICE")
	}
}

// TestUpdate_Returning_OverWire confirms RETURNING reflects the
// post-update row.
func TestUpdate_Returning_OverWire(t *testing.T) {
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
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name) VALUES (1, 'a'), (2, 'b'), (3, 'c')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	rows, err := pool.Query(ctx, `UPDATE accounts SET name = $1 WHERE id >= $2 RETURNING id, name`, "X", int32(2))
	if err != nil {
		t.Fatalf("UPDATE RETURNING: %v", err)
	}
	type acct struct {
		ID   int32
		Name string
	}
	var got []acct
	for rows.Next() {
		var a acct
		if err := rows.Scan(&a.ID, &a.Name); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		got = append(got, a)
	}
	sort.Slice(got, func(i, j int) bool { return got[i].ID < got[j].ID })
	want := []acct{{2, "X"}, {3, "X"}}
	if len(got) != len(want) {
		t.Fatalf("rows: got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("row %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestUpdate_UniqueViolation_OverWire confirms changing a UNIQUE column
// to a colliding value rejects with SQLSTATE 23505.
func TestUpdate_UniqueViolation_OverWire(t *testing.T) {
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
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name) VALUES (1, 'a'), (2, 'b')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	_, err = pool.Exec(ctx, `UPDATE accounts SET id = $1 WHERE id = $2`, int32(1), int32(2))
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("err type: got %T (%v), want *pgconn.PgError", err, err)
	}
	if pgErr.Code != "23505" {
		t.Errorf("SQLSTATE: got %q, want %q", pgErr.Code, "23505")
	}

	// Original IDs unchanged.
	var n int32
	if err := pool.QueryRow(ctx, `SELECT id FROM accounts WHERE name = 'b'`).Scan(&n); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if n != 2 {
		t.Errorf("id stayed: got %d, want 2", n)
	}
}
