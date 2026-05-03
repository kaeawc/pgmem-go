package exec_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/kaeawc/pgmem-go/catalog"
	"github.com/kaeawc/pgmem-go/exec"
	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/storage"
	"github.com/kaeawc/pgmem-go/types"
)

// runInsertReturning fully drains an insert plan and returns whatever
// rows the RETURNING clause emitted, plus any non-EOF error.
func runInsertReturning(t *testing.T, plan *ir.Insert) ([]exec.Row, error) {
	t.Helper()
	sch := catalog.NewSchema()
	eng := storage.NewEngine()
	if err := sch.CreateTable(catalog.Table{
		Name: "events",
		Columns: []catalog.Column{
			{Name: "id", Type: types.Int4, NotNull: true, Unique: true},
			{Name: "kind", Type: types.Text, NotNull: true},
		},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	eng.CreateTable("events", 2)

	txn, _ := eng.Begin(context.Background())
	defer txn.Rollback()
	op, err := exec.Build(plan, &exec.Env{Schema: sch, Engine: eng, Txn: txn})
	if err != nil {
		return nil, err
	}
	defer op.Close()
	var out []exec.Row
	for {
		row, err := op.Next(context.Background())
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, row)
	}
}

// TestInsert_Returning_EmitsProjectedRows confirms RETURNING emits one
// row per inserted row, with each expression evaluated against the
// freshly-inserted row.
func TestInsert_Returning_EmitsProjectedRows(t *testing.T) {
	plan := &ir.Insert{
		Table:   "events",
		Columns: []string{"id", "kind"},
		Rows: [][]ir.Expr{
			{&ir.Literal{Value: int32(1), T: types.Int4}, &ir.Literal{Value: "click", T: types.Text}},
			{&ir.Literal{Value: int32(2), T: types.Int4}, &ir.Literal{Value: "view", T: types.Text}},
		},
		Returning: []ir.Expr{
			&ir.ColumnRef{Name: "id"},
			&ir.ColumnRef{Name: "kind"},
		},
		ReturningNames: []string{"id", "kind"},
	}
	rows, err := runInsertReturning(t, plan)
	if err != nil {
		t.Fatalf("runInsertReturning: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows: got %d, want 2", len(rows))
	}
	if rows[0][0] != int32(1) || rows[0][1] != "click" {
		t.Errorf("row 0: got %+v, want [1 click]", rows[0])
	}
	if rows[1][0] != int32(2) || rows[1][1] != "view" {
		t.Errorf("row 1: got %+v, want [2 view]", rows[1])
	}
}

// TestInsert_Returning_PartialColumns lets the RETURNING clause name a
// subset of columns — the common sqlc pattern is `RETURNING id` to
// pick up an auto-assigned key.
func TestInsert_Returning_PartialColumns(t *testing.T) {
	plan := &ir.Insert{
		Table:          "events",
		Columns:        []string{"id", "kind"},
		Rows:           [][]ir.Expr{{&ir.Literal{Value: int32(99), T: types.Int4}, &ir.Literal{Value: "ping", T: types.Text}}},
		Returning:      []ir.Expr{&ir.ColumnRef{Name: "id"}},
		ReturningNames: []string{"id"},
	}
	rows, err := runInsertReturning(t, plan)
	if err != nil {
		t.Fatalf("runInsertReturning: %v", err)
	}
	if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0] != int32(99) {
		t.Errorf("rows: got %v, want [[99]]", rows)
	}
}

// TestInsert_Returning_EmptyOnFailure makes sure that when the INSERT
// itself fails (e.g. UNIQUE violation), no RETURNING rows leak out.
func TestInsert_Returning_EmptyOnFailure(t *testing.T) {
	first := &ir.Insert{
		Table:   "events",
		Columns: []string{"id", "kind"},
		Rows:    [][]ir.Expr{{&ir.Literal{Value: int32(1), T: types.Int4}, &ir.Literal{Value: "ping", T: types.Text}}},
	}
	if _, err := runInsertReturning(t, first); err != nil {
		t.Fatalf("seed insert: %v", err)
	}

	// Re-using the same fixture would create a fresh table, so for the
	// dup case run it inline with our own fixture.
	sch := catalog.NewSchema()
	eng := storage.NewEngine()
	_ = sch.CreateTable(catalog.Table{
		Name: "events",
		Columns: []catalog.Column{
			{Name: "id", Type: types.Int4, NotNull: true, Unique: true},
			{Name: "kind", Type: types.Text, NotNull: true},
		},
	})
	eng.CreateTable("events", 2)
	st, _ := eng.Table("events")
	st.Insert(storage.Row{int32(1), "ping"}) // pre-existing row to clash with

	dup := &ir.Insert{
		Table:          "events",
		Columns:        []string{"id", "kind"},
		Rows:           [][]ir.Expr{{&ir.Literal{Value: int32(1), T: types.Int4}, &ir.Literal{Value: "again", T: types.Text}}},
		Returning:      []ir.Expr{&ir.ColumnRef{Name: "id"}},
		ReturningNames: []string{"id"},
	}
	txn, _ := eng.Begin(context.Background())
	defer txn.Rollback()
	op, err := exec.Build(dup, &exec.Env{Schema: sch, Engine: eng, Txn: txn})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer op.Close()
	row, err := op.Next(context.Background())
	if err == nil {
		t.Fatalf("Next: want error, got row %+v", row)
	}
	var sqlErr *exec.SQLError
	if !errors.As(err, &sqlErr) || sqlErr.Code != "23505" {
		t.Errorf("err: got %v, want 23505", err)
	}
	if row != nil {
		t.Errorf("row: got %+v, want nil on failure", row)
	}
}
