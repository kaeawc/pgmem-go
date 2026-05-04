package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func generatedPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

func TestGenerated_ComputedOnInsert(t *testing.T) {
	pool, ctx, cleanup := generatedPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE rect (
		  id     int PRIMARY KEY,
		  width  int NOT NULL,
		  height int NOT NULL,
		  area   int GENERATED ALWAYS AS (width * height) STORED
		)
	`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO rect (id, width, height) VALUES (1, 4, 5)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	var area int32
	if err := pool.QueryRow(ctx, `SELECT area FROM rect WHERE id = 1`).Scan(&area); err != nil {
		t.Fatalf("select: %v", err)
	}
	if area != 20 {
		t.Errorf("area = %d, want 20", area)
	}
}

func TestGenerated_RecomputedOnUpdate(t *testing.T) {
	pool, ctx, cleanup := generatedPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE rect (
		  id     int PRIMARY KEY,
		  width  int NOT NULL,
		  height int NOT NULL,
		  area   int GENERATED ALWAYS AS (width * height) STORED
		)
	`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO rect (id, width, height) VALUES (1, 2, 3)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE rect SET width = 7 WHERE id = 1`); err != nil {
		t.Fatalf("UPDATE: %v", err)
	}
	var area int32
	if err := pool.QueryRow(ctx, `SELECT area FROM rect WHERE id = 1`).Scan(&area); err != nil {
		t.Fatalf("select: %v", err)
	}
	if area != 21 {
		t.Errorf("area = %d, want 21", area)
	}
}

func TestGenerated_RejectsExplicitInsert(t *testing.T) {
	pool, ctx, cleanup := generatedPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE rect (
		  id    int PRIMARY KEY,
		  side  int NOT NULL,
		  sq    int GENERATED ALWAYS AS (side * side) STORED
		)
	`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO rect (id, side, sq) VALUES (1, 4, 99)`); err == nil {
		t.Fatalf("expected error inserting into generated column")
	}
}

func TestGenerated_RejectsExplicitUpdate(t *testing.T) {
	pool, ctx, cleanup := generatedPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE rect (
		  id    int PRIMARY KEY,
		  side  int NOT NULL,
		  sq    int GENERATED ALWAYS AS (side * side) STORED
		)
	`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO rect (id, side) VALUES (1, 4)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE rect SET sq = 100 WHERE id = 1`); err == nil {
		t.Fatalf("expected error updating generated column")
	}
}

func TestGenerated_AcceptsDefaultMarker(t *testing.T) {
	pool, ctx, cleanup := generatedPool(t)
	defer cleanup()
	// Inserting DEFAULT for a generated column is what migration
	// tools may emit; the parser already accepts DEFAULT in VALUES,
	// so we just need to make sure the executor doesn't reject it
	// (the column is not "user-supplied" when DEFAULT is given).
	if _, err := pool.Exec(ctx, `
		CREATE TABLE rect (
		  id    int PRIMARY KEY,
		  side  int NOT NULL,
		  sq    int GENERATED ALWAYS AS (side * side) STORED
		)
	`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO rect (id, side, sq) VALUES (1, 5, DEFAULT)`); err != nil {
		t.Fatalf("INSERT with DEFAULT: %v", err)
	}
	var sq int32
	if err := pool.QueryRow(ctx, `SELECT sq FROM rect WHERE id = 1`).Scan(&sq); err != nil {
		t.Fatalf("select: %v", err)
	}
	if sq != 25 {
		t.Errorf("sq = %d, want 25", sq)
	}
}

func TestGenerated_TextConcat(t *testing.T) {
	pool, ctx, cleanup := generatedPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE people (
		  id        int PRIMARY KEY,
		  first     text NOT NULL,
		  last      text NOT NULL,
		  full_name text GENERATED ALWAYS AS (first || ' ' || last) STORED
		)
	`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO people (id, first, last) VALUES (1, 'Ada', 'Lovelace')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	var name string
	if err := pool.QueryRow(ctx, `SELECT full_name FROM people WHERE id = 1`).Scan(&name); err != nil {
		t.Fatalf("select: %v", err)
	}
	if name != "Ada Lovelace" {
		t.Errorf("full_name = %q, want %q", name, "Ada Lovelace")
	}
}
