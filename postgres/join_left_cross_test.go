package postgres_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func setupJoinTables(t *testing.T, pool *pgxpool.Pool, ctx context.Context) {
	t.Helper()
	if _, err := pool.Exec(ctx, `CREATE TABLE customers (id int PRIMARY KEY, name text NOT NULL)`); err != nil {
		t.Fatalf("CREATE customers: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE orders (id int PRIMARY KEY, customer_id int NOT NULL, total bigint NOT NULL)`); err != nil {
		t.Fatalf("CREATE orders: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO customers (id, name) VALUES (1, 'alice'), (2, 'bob'), (3, 'carol')`); err != nil {
		t.Fatalf("INSERT customers: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO orders (id, customer_id, total) VALUES (10, 1, 100), (11, 2, 200)`); err != nil {
		t.Fatalf("INSERT orders: %v", err)
	}
	// Carol intentionally has no orders.
}

// TestLeftJoin_OverWire: the customer with no matching order shows up
// once with NULL total.
func TestLeftJoin_OverWire(t *testing.T) {
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

	setupJoinTables(t, pool, ctx)

	type row struct {
		Name  string
		Total *int64 // pointer to capture NULL
	}
	rows, err := pool.Query(ctx,
		`SELECT customers.name, orders.total FROM customers LEFT JOIN orders ON customers.id = orders.customer_id`,
	)
	if err != nil {
		t.Fatalf("LEFT JOIN: %v", err)
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
	sort.Slice(got, func(i, j int) bool { return got[i].Name < got[j].Name })
	if len(got) != 3 {
		t.Fatalf("rows: got %d, want 3 (alice/bob/carol)", len(got))
	}
	if got[0].Name != "alice" || got[0].Total == nil || *got[0].Total != 100 {
		t.Errorf("alice: got %+v", got[0])
	}
	if got[1].Name != "bob" || got[1].Total == nil || *got[1].Total != 200 {
		t.Errorf("bob: got %+v", got[1])
	}
	if got[2].Name != "carol" || got[2].Total != nil {
		t.Errorf("carol: got %+v, want nil total", got[2])
	}
}

// TestCrossJoin_OverWire: 3 customers x 2 orders = 6 rows, no
// condition required.
func TestCrossJoin_OverWire(t *testing.T) {
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

	setupJoinTables(t, pool, ctx)

	rows, err := pool.Query(ctx,
		`SELECT customers.id FROM customers CROSS JOIN orders`,
	)
	if err != nil {
		t.Fatalf("CROSS JOIN: %v", err)
	}
	count := 0
	for rows.Next() {
		var n int32
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if count != 6 {
		t.Errorf("rows: got %d, want 6", count)
	}
}
