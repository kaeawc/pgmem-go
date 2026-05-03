package postgres_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

// TestInnerJoin_OverWire is the wire-level happy path: two tables,
// one parent-child relationship, INNER JOIN over an equality condition.
func TestInnerJoin_OverWire(t *testing.T) {
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

	if _, err := pool.Exec(ctx, `CREATE TABLE customers (id int PRIMARY KEY, name text NOT NULL)`); err != nil {
		t.Fatalf("CREATE customers: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE orders (id int PRIMARY KEY, customer_id int NOT NULL, total bigint NOT NULL)`); err != nil {
		t.Fatalf("CREATE orders: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO customers (id, name) VALUES (1, 'alice'), (2, 'bob'), (3, 'carol')`); err != nil {
		t.Fatalf("INSERT customers: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO orders (id, customer_id, total) VALUES (10, 1, 100), (11, 1, 50), (12, 2, 200)`); err != nil {
		t.Fatalf("INSERT orders: %v", err)
	}

	type row struct {
		Name  string
		Total int64
	}
	rows, err := pool.Query(ctx,
		`SELECT customers.name, orders.total FROM customers INNER JOIN orders ON customers.id = orders.customer_id`,
	)
	if err != nil {
		t.Fatalf("JOIN: %v", err)
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.Name, &r.Total); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	sort.Slice(got, func(i, j int) bool {
		if got[i].Name != got[j].Name {
			return got[i].Name < got[j].Name
		}
		return got[i].Total < got[j].Total
	})
	want := []row{
		{"alice", 50},
		{"alice", 100},
		{"bob", 200},
	}
	if len(got) != len(want) {
		t.Fatalf("rows: got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("row %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestInnerJoin_WithWhereAndOrder confirms JOIN composes with WHERE,
// ORDER BY, LIMIT in the way sqlc-generated query code uses them.
func TestInnerJoin_WithWhereAndOrder(t *testing.T) {
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

	if _, err := pool.Exec(ctx, `CREATE TABLE customers (id int PRIMARY KEY, name text NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE orders (id int PRIMARY KEY, customer_id int NOT NULL, total bigint NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO customers (id, name) VALUES (1, 'alice'), (2, 'bob')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO orders (id, customer_id, total) VALUES (10, 1, 100), (11, 1, 50), (12, 2, 200), (13, 1, 75)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	type out struct {
		Total int64
	}
	rows, err := pool.Query(ctx,
		`SELECT orders.total FROM customers JOIN orders ON customers.id = orders.customer_id WHERE customers.name = $1 ORDER BY orders.total DESC LIMIT 2`,
		"alice",
	)
	if err != nil {
		t.Fatalf("JOIN: %v", err)
	}
	var got []out
	for rows.Next() {
		var o out
		if err := rows.Scan(&o.Total); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		got = append(got, o)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	want := []out{{100}, {75}}
	if len(got) != len(want) {
		t.Fatalf("rows: got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("row %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}
