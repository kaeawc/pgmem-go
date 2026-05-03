package postgres_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

// TestDelete_OverWire is the wire-level happy path: seed a few rows,
// DELETE one by id, confirm storage and the CommandComplete tag.
func TestDelete_OverWire(t *testing.T) {
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

	if _, err := pool.Exec(ctx, `CREATE TABLE items (id int PRIMARY KEY, label text)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO items (id, label) VALUES (1, 'a'), (2, 'b'), (3, 'c')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	tag, err := pool.Exec(ctx, `DELETE FROM items WHERE id = $1`, int32(2))
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Errorf("RowsAffected: got %d, want 1", tag.RowsAffected())
	}

	rows, err := pool.Query(ctx, `SELECT id FROM items`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	var got []int32
	for rows.Next() {
		var n int32
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		got = append(got, n)
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	want := []int32{1, 3}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("survivors: got %v, want %v", got, want)
	}
}

// TestDelete_Returning_OverWire confirms DELETE ... RETURNING surfaces
// the deleted rows back to pgx the way INSERT ... RETURNING does.
func TestDelete_Returning_OverWire(t *testing.T) {
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

	if _, err := pool.Exec(ctx, `CREATE TABLE items (id int PRIMARY KEY, label text)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO items (id, label) VALUES (1, 'a'), (2, 'b'), (3, 'c')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	rows, err := pool.Query(ctx, `DELETE FROM items WHERE id >= $1 RETURNING id, label`, int32(2))
	if err != nil {
		t.Fatalf("DELETE RETURNING: %v", err)
	}
	type item struct {
		ID    int32
		Label string
	}
	var got []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.ID, &it.Label); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		got = append(got, it)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	sort.Slice(got, func(i, j int) bool { return got[i].ID < got[j].ID })
	want := []item{{2, "b"}, {3, "c"}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("row %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}
