// Package inventory_test exercises the sqlc-generated query layer
// against an embedded pgmem-go server. The schema covers
// self-referential FKs (categories tree), GROUP BY rollups, a CTE
// over those rollups, and CASE-driven status flags.
package inventory_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/examples/inventory/db"
	"github.com/kaeawc/pgmem-go/postgres"
)

func setup(t *testing.T) (*db.Queries, context.Context, func()) {
	t.Helper()
	srv, err := postgres.Start(t)
	if err != nil {
		t.Fatalf("postgres.Start: %v", err)
	}
	srv.SetNow(time.Date(2024, 7, 15, 12, 0, 0, 0, time.UTC))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	pool, err := pgxpool.New(ctx, srv.DSN())
	if err != nil {
		cancel()
		t.Fatalf("pgxpool.New: %v", err)
	}
	schemaBytes, err := os.ReadFile(filepath.Join("schema.sql"))
	if err != nil {
		pool.Close()
		cancel()
		t.Fatalf("read schema: %v", err)
	}
	for _, stmt := range strings.Split(string(schemaBytes), ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := pool.Exec(ctx, stmt); err != nil {
			pool.Close()
			cancel()
			t.Fatalf("schema %q: %v", stmt, err)
		}
	}
	return db.New(pool), ctx, func() { pool.Close(); cancel() }
}

// fixture seeds a small store: one category tree, two warehouses, two
// products, and a handful of stock transactions.
func fixture(t *testing.T, q *db.Queries, ctx context.Context) (rootID, leafID, prod1ID, prod2ID, wh1ID, wh2ID int64) {
	t.Helper()
	root, err := q.AddCategory(ctx, db.AddCategoryParams{ParentID: nil, Name: "tools"})
	if err != nil {
		t.Fatalf("AddCategory root: %v", err)
	}
	leaf, err := q.AddCategory(ctx, db.AddCategoryParams{ParentID: &root.ID, Name: "drills"})
	if err != nil {
		t.Fatalf("AddCategory leaf: %v", err)
	}
	wh1, err := q.AddWarehouse(ctx, "north")
	if err != nil {
		t.Fatalf("AddWarehouse north: %v", err)
	}
	wh2, err := q.AddWarehouse(ctx, "south")
	if err != nil {
		t.Fatalf("AddWarehouse south: %v", err)
	}
	p1, err := q.AddProduct(ctx, db.AddProductParams{
		CategoryID: leaf.ID, Name: "hammer", Threshold: 5,
	})
	if err != nil {
		t.Fatalf("AddProduct hammer: %v", err)
	}
	p2, err := q.AddProduct(ctx, db.AddProductParams{
		CategoryID: leaf.ID, Name: "screwdriver", Threshold: 10,
	})
	if err != nil {
		t.Fatalf("AddProduct screwdriver: %v", err)
	}
	now := time.Date(2024, 7, 15, 12, 0, 0, 0, time.UTC)
	for _, tx := range []db.RecordTxnParams{
		{ProductID: p1.ID, WarehouseID: wh1.ID, Delta: 7, OccurredAt: now},
		{ProductID: p1.ID, WarehouseID: wh1.ID, Delta: -3, OccurredAt: now},
		{ProductID: p2.ID, WarehouseID: wh2.ID, Delta: 4, OccurredAt: now},
	} {
		if _, err := q.RecordTxn(ctx, tx); err != nil {
			t.Fatalf("RecordTxn: %v", err)
		}
	}
	return root.ID, leaf.ID, p1.ID, p2.ID, wh1.ID, wh2.ID
}

// TestOnHandByProduct exercises a sum() GROUP BY rollup.
func TestOnHandByProduct(t *testing.T) {
	q, ctx, cleanup := setup(t)
	defer cleanup()
	_, _, p1, p2, _, _ := fixture(t, q, ctx)
	rows, err := q.OnHandByProduct(ctx)
	if err != nil {
		t.Fatalf("OnHandByProduct: %v", err)
	}
	got := map[int64]int32{}
	for _, r := range rows {
		got[r.ProductID] = r.Qty
	}
	if got[p1] != 4 { // 7 - 3
		t.Errorf("p1 qty = %d, want 4", got[p1])
	}
	if got[p2] != 4 {
		t.Errorf("p2 qty = %d, want 4", got[p2])
	}
}

// TestLowStockReport exercises a CTE on top of the GROUP BY plus a
// CASE expression that flags below-threshold products. p1's threshold
// is 5 but its on-hand is 4 → "low"; p2's threshold is 10 and
// on-hand 4 → "low" as well.
func TestLowStockReport(t *testing.T) {
	q, ctx, cleanup := setup(t)
	defer cleanup()
	fixture(t, q, ctx)
	rows, err := q.LowStockReport(ctx)
	if err != nil {
		t.Fatalf("LowStockReport: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for _, r := range rows {
		if r.Status != "low" {
			t.Errorf("%s: status = %q, want low (qty=%d)", r.Name, r.Status, r.Qty)
		}
	}
}

// TestLowStockReport_OkBranch exercises the CASE's "ok" arm. With no
// transactions for a freshly-added product, on_hand is NULL and
// coalesce(on_hand, 0) is 0 — but if threshold is 0 the row passes.
// We override that here by adding stock above the threshold and
// re-querying.
func TestLowStockReport_OkBranch(t *testing.T) {
	q, ctx, cleanup := setup(t)
	defer cleanup()
	_, leafID, _, _, wh1, _ := fixture(t, q, ctx)
	// Add a fresh product with a low threshold and plenty of stock.
	stocked, err := q.AddProduct(ctx, db.AddProductParams{
		CategoryID: leafID, Name: "wrench", Threshold: 1,
	})
	if err != nil {
		t.Fatalf("AddProduct wrench: %v", err)
	}
	if _, err := q.RecordTxn(ctx, db.RecordTxnParams{
		ProductID: stocked.ID, WarehouseID: wh1, Delta: 50,
		OccurredAt: time.Date(2024, 7, 15, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("RecordTxn wrench: %v", err)
	}
	rows, err := q.LowStockReport(ctx)
	if err != nil {
		t.Fatalf("LowStockReport: %v", err)
	}
	statuses := map[string]string{}
	for _, r := range rows {
		statuses[r.Name] = r.Status
	}
	if statuses["wrench"] != "ok" {
		t.Errorf("wrench: status = %q, want ok", statuses["wrench"])
	}
}

// TestWarehouseRollup exercises a second sum() GROUP BY shape.
func TestWarehouseRollup(t *testing.T) {
	q, ctx, cleanup := setup(t)
	defer cleanup()
	_, _, _, _, wh1, wh2 := fixture(t, q, ctx)
	rows, err := q.WarehouseRollup(ctx)
	if err != nil {
		t.Fatalf("WarehouseRollup: %v", err)
	}
	got := map[int64]int32{}
	for _, r := range rows {
		got[r.WarehouseID] = r.Qty
	}
	if got[wh1] != 4 {
		t.Errorf("wh1 = %d, want 4", got[wh1])
	}
	if got[wh2] != 4 {
		t.Errorf("wh2 = %d, want 4", got[wh2])
	}
}

// TestChildCategories exercises the self-referential FK lookup. The
// `tools` category has one child `drills`; querying with parent_id
// pointing at root must return that child only.
func TestChildCategories(t *testing.T) {
	q, ctx, cleanup := setup(t)
	defer cleanup()
	rootID, leafID, _, _, _, _ := fixture(t, q, ctx)
	got, err := q.ChildCategories(ctx, &rootID)
	if err != nil {
		t.Fatalf("ChildCategories: %v", err)
	}
	if len(got) != 1 || got[0].ID != leafID || got[0].Name != "drills" {
		t.Errorf("got %+v, want one row {leaf, drills}", got)
	}
}
