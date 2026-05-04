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

func groupByPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	if _, err := pool.Exec(ctx, `CREATE TABLE sales (id int PRIMARY KEY, region text NOT NULL, amount int NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sales (id, region, amount) VALUES
		(1, 'east', 10),
		(2, 'east', 20),
		(3, 'west', 5),
		(4, 'west', 30),
		(5, 'south', 100)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestGroupBy_SumPerRegion(t *testing.T) {
	pool, ctx, cleanup := groupByPool(t)
	defer cleanup()

	type row struct {
		Region string
		Sum    int64
	}
	rows, err := pool.Query(ctx, `SELECT region, sum(amount) FROM sales GROUP BY region`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.Region, &r.Sum); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		got = append(got, r)
	}
	sort.Slice(got, func(i, j int) bool { return got[i].Region < got[j].Region })
	want := []row{
		{"east", 30},
		{"south", 100},
		{"west", 35},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("row %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestGroupBy_CountAndOrder: GROUP BY + ORDER BY over the aggregated
// output. ORDER BY must see the group-aware columns, not the raw FROM
// columns — sits above the Aggregate.
func TestGroupBy_CountAndOrder(t *testing.T) {
	pool, ctx, cleanup := groupByPool(t)
	defer cleanup()

	rows, err := pool.Query(ctx,
		`SELECT region, count(*) FROM sales GROUP BY region ORDER BY region`,
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	var got []string
	for rows.Next() {
		var region string
		var n int64
		if err := rows.Scan(&region, &n); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		got = append(got, region)
	}
	want := []string{"east", "south", "west"} // alphabetical
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("row %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// TestGroupBy_HavingFilter: HAVING is a post-aggregate predicate.
func TestGroupBy_HavingFilter(t *testing.T) {
	pool, ctx, cleanup := groupByPool(t)
	defer cleanup()

	rows, err := pool.Query(ctx,
		`SELECT region, sum(amount) FROM sales GROUP BY region HAVING sum(amount) > 30 ORDER BY region`,
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	type row struct {
		Region string
		Sum    int64
	}
	var got []row
	for rows.Next() {
		var r row
		_ = rows.Scan(&r.Region, &r.Sum)
		got = append(got, r)
	}
	want := []row{
		{"south", 100},
		{"west", 35},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("row %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestGroupBy_RejectsBareColumnNotInGroupBy: SELECT on a column that
// isn't in GROUP BY and isn't aggregated must fail.
func TestGroupBy_RejectsBareColumnNotInGroupBy(t *testing.T) {
	pool, ctx, cleanup := groupByPool(t)
	defer cleanup()
	_, err := pool.Exec(ctx, `SELECT id, sum(amount) FROM sales GROUP BY region`)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("err type: got %T (%v), want *pgconn.PgError", err, err)
	}
}
