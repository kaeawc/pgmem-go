// Package todo_test exercises the sqlc-generated query layer against
// an embedded pgmem-go server. The shape of these tests is the
// blueprint for the other examples — schema is read from schema.sql,
// each generated query runs at least once, and the assertions confirm
// the dialect features each query relies on.
package todo_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/examples/todo/db"
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

// TestUpsertList covers ON CONFLICT DO UPDATE: the second call with
// the same id should replace the existing name and return the
// updated row.
func TestUpsertList(t *testing.T) {
	q, ctx, cleanup := setup(t)
	defer cleanup()
	now := time.Date(2024, 7, 15, 12, 0, 0, 0, time.UTC)

	first, err := q.UpsertList(ctx, db.UpsertListParams{
		ID: 1, Name: "groceries", CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if first.Name != "groceries" {
		t.Errorf("first.Name = %q", first.Name)
	}

	updated, err := q.UpsertList(ctx, db.UpsertListParams{
		ID: 1, Name: "shopping", CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if updated.Name != "shopping" {
		t.Errorf("post-conflict name = %q, want shopping", updated.Name)
	}
	if updated.ID != 1 {
		t.Errorf("ID changed: %d", updated.ID)
	}
}

// TestSoftDeleteFiltersOut covers `WHERE deleted_at IS NULL` filtering.
func TestSoftDeleteFiltersOut(t *testing.T) {
	q, ctx, cleanup := setup(t)
	defer cleanup()
	now := time.Date(2024, 7, 15, 12, 0, 0, 0, time.UTC)

	for i, name := range []string{"a", "b", "c"} {
		if _, err := q.UpsertList(ctx, db.UpsertListParams{
			ID: int64(i + 1), Name: name, CreatedAt: now,
		}); err != nil {
			t.Fatalf("upsert %s: %v", name, err)
		}
	}
	if err := q.SoftDeleteList(ctx, db.SoftDeleteListParams{
		ID: 2, DeletedAt: &now,
	}); err != nil {
		t.Fatalf("SoftDeleteList: %v", err)
	}
	active, err := q.ActiveLists(ctx)
	if err != nil {
		t.Fatalf("ActiveLists: %v", err)
	}
	if len(active) != 2 {
		t.Errorf("active = %d, want 2 (b deleted)", len(active))
	}
	for _, l := range active {
		if l.ID == 2 {
			t.Errorf("deleted list still active: %+v", l)
		}
	}
}

// TestItemsDueWithin covers timestamp + interval arithmetic. The
// query filters items whose due_at is within one day of the supplied
// reference time.
func TestItemsDueWithin(t *testing.T) {
	q, ctx, cleanup := setup(t)
	defer cleanup()
	now := time.Date(2024, 7, 15, 12, 0, 0, 0, time.UTC)
	if _, err := q.UpsertList(ctx, db.UpsertListParams{ID: 1, Name: "main", CreatedAt: now}); err != nil {
		t.Fatalf("upsert list: %v", err)
	}
	due12h := now.Add(12 * time.Hour)
	due48h := now.Add(48 * time.Hour)
	noDue := (*time.Time)(nil)
	for _, p := range []db.AddItemParams{
		{ListID: 1, Title: "soon", DueAt: &due12h, CreatedAt: now},
		{ListID: 1, Title: "later", DueAt: &due48h, CreatedAt: now},
		{ListID: 1, Title: "anytime", DueAt: noDue, CreatedAt: now},
	} {
		if _, err := q.AddItem(ctx, p); err != nil {
			t.Fatalf("AddItem %s: %v", p.Title, err)
		}
	}
	got, err := q.ItemsDueWithin(ctx, now)
	if err != nil {
		t.Fatalf("ItemsDueWithin: %v", err)
	}
	if len(got) != 1 || got[0].Title != "soon" {
		t.Errorf("got %d rows: %+v", len(got), got)
	}
}

// TestListCompletion covers GROUP BY + bool_and across the rows of
// each list. The all-done case returns true; mixed returns false.
func TestListCompletion(t *testing.T) {
	q, ctx, cleanup := setup(t)
	defer cleanup()
	now := time.Date(2024, 7, 15, 12, 0, 0, 0, time.UTC)

	for i, name := range []string{"alpha", "beta"} {
		if _, err := q.UpsertList(ctx, db.UpsertListParams{
			ID: int64(i + 1), Name: name, CreatedAt: now,
		}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	addItems := func(listID int64, n int) {
		for i := 0; i < n; i++ {
			if _, err := q.AddItem(ctx, db.AddItemParams{
				ListID: listID, Title: "item", CreatedAt: now,
			}); err != nil {
				t.Fatalf("AddItem: %v", err)
			}
		}
	}
	addItems(1, 3)
	addItems(2, 2)

	// Complete every item in list 1, leave list 2 alone.
	rows1, err := q.ListCompletion(ctx)
	if err != nil {
		t.Fatalf("ListCompletion pre: %v", err)
	}
	for _, r := range rows1 {
		if r.AllDone {
			t.Errorf("list %d marked done before any complete: %+v", r.ListID, r)
		}
	}

	// Complete each item in list 1 by id (1, 2, 3).
	for id := int64(1); id <= 3; id++ {
		if err := q.CompleteItem(ctx, id); err != nil {
			t.Fatalf("CompleteItem %d: %v", id, err)
		}
	}

	rows2, err := q.ListCompletion(ctx)
	if err != nil {
		t.Fatalf("ListCompletion post: %v", err)
	}
	got := map[int64]bool{}
	for _, r := range rows2 {
		got[r.ListID] = r.AllDone
	}
	if !got[1] {
		t.Errorf("list 1 should be all_done after completing every item")
	}
	if got[2] {
		t.Errorf("list 2 should NOT be all_done — none of its items completed")
	}
}
