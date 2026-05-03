package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

// TestUUID_Column_GenAndRoundTrip exercises the most common sqlc
// pattern: a uuid PRIMARY KEY DEFAULT gen_random_uuid()-shaped insert
// where the client supplies the uuid via gen_random_uuid() in the
// VALUES clause itself, gets it back via RETURNING, and reads it.
func TestUUID_Column_GenAndRoundTrip(t *testing.T) {
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

	if _, err := pool.Exec(ctx, `CREATE TABLE rows (id uuid PRIMARY KEY, label text NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	var id [16]byte
	if err := pool.QueryRow(ctx,
		`INSERT INTO rows (id, label) VALUES (gen_random_uuid(), $1) RETURNING id`,
		"alpha",
	).Scan(&id); err != nil {
		t.Fatalf("INSERT RETURNING: %v", err)
	}
	if id == ([16]byte{}) {
		t.Fatal("returned id was zero")
	}
	if (id[6] >> 4) != 0x4 {
		t.Errorf("version nibble: got %x, want 4", id[6]>>4)
	}

	// Read it back via WHERE.
	var label string
	if err := pool.QueryRow(ctx, `SELECT label FROM rows WHERE id = $1`, id).Scan(&label); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if label != "alpha" {
		t.Errorf("label: got %q, want alpha", label)
	}
}

// TestUUID_GenInSelect runs gen_random_uuid() as a bare select item to
// make sure the function infrastructure works without an INSERT context.
func TestUUID_GenInSelect(t *testing.T) {
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

	var u [16]byte
	if err := conn.QueryRow(ctx, `SELECT gen_random_uuid()`).Scan(&u); err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if u == ([16]byte{}) {
		t.Errorf("got zero uuid")
	}
}

// TestUUID_UnknownFunctionErrors confirms an unknown builtin surfaces
// at the wire as a real PG error rather than a hang or panic.
func TestUUID_UnknownFunctionErrors(t *testing.T) {
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

	_, err = conn.Exec(ctx, `SELECT no_such_function()`)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("err type: got %T (%v), want *pgconn.PgError", err, err)
	}
}
