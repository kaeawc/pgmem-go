package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func windowPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	if _, err := pool.Exec(ctx,
		`INSERT INTO sales (id, region, amount) VALUES
			(1, 'east', 100),
			(2, 'east', 90),
			(3, 'east', 100),
			(4, 'west', 200),
			(5, 'west', 50)`,
	); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

type winRow struct {
	id     int32
	region string
	amount int32
	r      int64
}

func collectWindow(t *testing.T, pool *pgxpool.Pool, ctx context.Context, sql string) []winRow {
	t.Helper()
	rows, err := pool.Query(ctx, sql)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	defer rows.Close()
	var out []winRow
	for rows.Next() {
		var r winRow
		if err := rows.Scan(&r.id, &r.region, &r.amount, &r.r); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, r)
	}
	return out
}

func TestWindow_RowNumber_PartitionOrder(t *testing.T) {
	pool, ctx, cleanup := windowPool(t)
	defer cleanup()
	got := collectWindow(t, pool, ctx, `
		SELECT id, region, amount,
		       row_number() OVER (PARTITION BY region ORDER BY amount DESC) AS rn
		FROM sales ORDER BY region, rn`)
	// east: 1 (100), 3 (100), 2 (90) — row_number ties are broken by
	// stable sort on input order, so id=1 ranks first within the
	// 100-tied group, id=3 second.
	want := []winRow{
		{1, "east", 100, 1},
		{3, "east", 100, 2},
		{2, "east", 90, 3},
		{4, "west", 200, 1},
		{5, "west", 50, 2},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestWindow_Rank_TiesShareRank(t *testing.T) {
	pool, ctx, cleanup := windowPool(t)
	defer cleanup()
	got := collectWindow(t, pool, ctx, `
		SELECT id, region, amount,
		       rank() OVER (PARTITION BY region ORDER BY amount DESC) AS rk
		FROM sales ORDER BY region, id`)
	// east: id=1/3 both 100 → rank 1, id=2 90 → rank 3 (gap).
	want := []winRow{
		{1, "east", 100, 1},
		{2, "east", 90, 3},
		{3, "east", 100, 1},
		{4, "west", 200, 1},
		{5, "west", 50, 2},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestWindow_DenseRank_NoGap(t *testing.T) {
	pool, ctx, cleanup := windowPool(t)
	defer cleanup()
	got := collectWindow(t, pool, ctx, `
		SELECT id, region, amount,
		       dense_rank() OVER (PARTITION BY region ORDER BY amount DESC) AS dr
		FROM sales ORDER BY region, id`)
	// east: 100/100 share rank 1, 90 is rank 2 (no gap).
	want := []winRow{
		{1, "east", 100, 1},
		{2, "east", 90, 2},
		{3, "east", 100, 1},
		{4, "west", 200, 1},
		{5, "west", 50, 2},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestWindow_NoPartition(t *testing.T) {
	pool, ctx, cleanup := windowPool(t)
	defer cleanup()
	got := collectWindow(t, pool, ctx, `
		SELECT id, region, amount,
		       row_number() OVER (ORDER BY amount DESC) AS rn
		FROM sales ORDER BY rn`)
	if len(got) != 5 || got[0].amount != 200 || got[4].amount != 50 {
		t.Errorf("got %v", got)
	}
}
