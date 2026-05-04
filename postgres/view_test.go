package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func viewPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	for _, sql := range []string{
		`CREATE TABLE orders (id int PRIMARY KEY, user_id int NOT NULL, amount int NOT NULL, status text NOT NULL)`,
		`INSERT INTO orders (id, user_id, amount, status) VALUES
			(1, 1, 100, 'paid'),
			(2, 1, 50, 'pending'),
			(3, 2, 200, 'paid'),
			(4, 2, 75, 'paid')`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestCreateView_FilteringView(t *testing.T) {
	pool, ctx, cleanup := viewPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx,
		`CREATE VIEW paid_orders AS SELECT * FROM orders WHERE status = 'paid'`,
	); err != nil {
		t.Fatalf("CREATE VIEW: %v", err)
	}
	rows, err := pool.Query(ctx, `SELECT id FROM paid_orders ORDER BY id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var got []int32
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, id)
	}
	want := []int32{1, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestCreateView_AggregateView(t *testing.T) {
	pool, ctx, cleanup := viewPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx,
		`CREATE VIEW user_totals AS
		 SELECT user_id, sum(amount)::bigint AS total
		 FROM orders WHERE status = 'paid' GROUP BY user_id`,
	); err != nil {
		t.Fatalf("CREATE VIEW: %v", err)
	}
	rows, err := pool.Query(ctx, `SELECT user_id, total FROM user_totals ORDER BY user_id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	got := map[int32]int64{}
	for rows.Next() {
		var u int32
		var total int64
		if err := rows.Scan(&u, &total); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[u] = total
	}
	if got[1] != 100 || got[2] != 275 {
		t.Errorf("got %v, want {1:100, 2:275}", got)
	}
}

func TestCreateView_QualifiedReference(t *testing.T) {
	pool, ctx, cleanup := viewPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx,
		`CREATE VIEW paid_orders AS SELECT id, user_id, amount FROM orders WHERE status = 'paid'`,
	); err != nil {
		t.Fatalf("CREATE VIEW: %v", err)
	}
	var n int64
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM paid_orders po WHERE po.user_id = 2`,
	).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 2 {
		t.Errorf("got %d, want 2", n)
	}
}

func TestDropView(t *testing.T) {
	pool, ctx, cleanup := viewPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE VIEW v AS SELECT * FROM orders`); err != nil {
		t.Fatalf("CREATE VIEW: %v", err)
	}
	if _, err := pool.Exec(ctx, `DROP VIEW v`); err != nil {
		t.Fatalf("DROP VIEW: %v", err)
	}
	_, err := pool.Query(ctx, `SELECT * FROM v`)
	if err == nil {
		t.Errorf("expected error querying dropped view")
	}
	// IF EXISTS form is a no-op for a missing view.
	if _, err := pool.Exec(ctx, `DROP VIEW IF EXISTS v`); err != nil {
		t.Errorf("DROP VIEW IF EXISTS: %v", err)
	}
}

func TestCreateView_ConflictWithTableName(t *testing.T) {
	pool, ctx, cleanup := viewPool(t)
	defer cleanup()
	_, err := pool.Exec(ctx, `CREATE VIEW orders AS SELECT * FROM orders`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42P07" {
		t.Errorf("got %v, want SQLSTATE 42P07", err)
	}
}
