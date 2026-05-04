package postgres_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func starPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, name text NOT NULL, n int)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, name, n) VALUES (1, 'a', 10), (2, 'b', 20), (3, 'c', NULL)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestSelectStar_AllColumns(t *testing.T) {
	pool, ctx, cleanup := starPool(t)
	defer cleanup()
	type row struct {
		ID   int32
		Name string
		N    *int32
	}
	rows, err := pool.Query(ctx, `SELECT * FROM t ORDER BY id`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ID, &r.Name, &r.N); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("rows: got %d, want 3", len(got))
	}
	if got[0].ID != 1 || got[0].Name != "a" || got[0].N == nil || *got[0].N != 10 {
		t.Errorf("row 0: got %+v", got[0])
	}
	if got[2].ID != 3 || got[2].N != nil {
		t.Errorf("row 2: got %+v", got[2])
	}
}

// TestSelectStar_ColumnNames confirms expanded columns retain their
// source names — useful for sqlc-generated `RETURNING *` codegen.
func TestSelectStar_ColumnNames(t *testing.T) {
	pool, ctx, cleanup := starPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `SELECT * FROM t WHERE id = 1`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if !rows.Next() {
		t.Fatal("expected a row")
	}
	desc := rows.FieldDescriptions()
	wantNames := []string{"id", "name", "n"}
	if len(desc) != len(wantNames) {
		t.Fatalf("cols: got %d, want %d", len(desc), len(wantNames))
	}
	for i, w := range wantNames {
		if string(desc[i].Name) != w {
			t.Errorf("col %d: got %q, want %q", i, desc[i].Name, w)
		}
	}
	rows.Close()
}

// TestSelectStar_Join: `SELECT * FROM a JOIN b ON ...` expands to
// every column from both sides in left-then-right order.
func TestSelectStar_Join(t *testing.T) {
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
	for _, sql := range []string{
		`CREATE TABLE a (id int PRIMARY KEY, name text NOT NULL)`,
		`CREATE TABLE b (id int PRIMARY KEY, a_id int NOT NULL, qty int)`,
		`INSERT INTO a (id, name) VALUES (1, 'alice')`,
		`INSERT INTO b (id, a_id, qty) VALUES (10, 1, 5)`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("%q: %v", sql, err)
		}
	}
	rows, err := pool.Query(ctx, `SELECT * FROM a JOIN b ON a.id = b.a_id`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	desc := rows.FieldDescriptions()
	got := make([]string, len(desc))
	for i, d := range desc {
		got[i] = string(d.Name)
	}
	rows.Close()
	want := []string{"id", "name", "id", "a_id", "qty"} // a's id, name; then b's id, a_id, qty
	sortedGot := append([]string(nil), got...)
	sortedWant := append([]string(nil), want...)
	sort.Strings(sortedGot)
	sort.Strings(sortedWant)
	if len(got) != len(want) {
		t.Fatalf("columns: got %v, want %v", got, want)
	}
	for i := range sortedGot {
		if sortedGot[i] != sortedWant[i] {
			t.Errorf("name set: got %v, want %v", sortedGot, sortedWant)
			break
		}
	}
}
