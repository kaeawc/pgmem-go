package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

// TestSelect1_Pgx is the M0 acceptance test: stock pgx connects to a
// fresh server, runs SELECT 1 in extended-query mode, and gets 1 back.
func TestSelect1_Pgx(t *testing.T) {
	srv, err := postgres.Start(t)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, srv.DSN())
	if err != nil {
		t.Fatalf("pgx.Connect: %v", err)
	}
	defer conn.Close(context.Background())

	var n int
	if err := conn.QueryRow(ctx, "SELECT 1").Scan(&n); err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if n != 1 {
		t.Fatalf("got %d, want 1", n)
	}
}

// TestSelect1_Pgxpool exercises the pool path the README example uses.
func TestSelect1_Pgxpool(t *testing.T) {
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

	var n int
	if err := pool.QueryRow(ctx, "SELECT 1").Scan(&n); err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if n != 1 {
		t.Fatalf("got %d, want 1", n)
	}
}

// TestGetUsers_SqlcStyle is the M1 acceptance test: a sqlc-generated
// GetUsers stand-in (a vanilla pool.Query loop) returns the seeded
// rows with the right Go types. This proves the parse → IR → exec →
// wire encode loop end-to-end.
func TestGetUsers_SqlcStyle(t *testing.T) {
	srv, err := postgres.Start(t)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := srv.Seed("users",
		[]any{int32(1), "alice"},
		[]any{int32(2), "bob"},
		[]any{int32(3), "carol"},
	); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, srv.DSN())
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	type user struct {
		ID   int32
		Name string
	}
	rows, err := pool.Query(ctx, "SELECT id, name FROM users")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	got := []user{}
	for rows.Next() {
		var u user
		if err := rows.Scan(&u.ID, &u.Name); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		got = append(got, u)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	want := []user{{1, "alice"}, {2, "bob"}, {3, "carol"}}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d (%v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("row %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestSimpleQueryProtocol forces simple-query mode (the path that bypasses
// Parse/Bind/Describe/Execute and goes straight to Query).
func TestSimpleQueryProtocol(t *testing.T) {
	srv, err := postgres.Start(t)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg, err := pgx.ParseConfig(srv.DSN())
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("ConnectConfig: %v", err)
	}
	defer conn.Close(context.Background())

	var n int
	if err := conn.QueryRow(ctx, "SELECT 1").Scan(&n); err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if n != 1 {
		t.Fatalf("got %d, want 1", n)
	}
}
