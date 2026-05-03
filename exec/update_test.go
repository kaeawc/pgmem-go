package exec_test

import (
	"context"
	"errors"
	"io"
	"sort"
	"testing"

	"github.com/kaeawc/pgmem-go/catalog"
	"github.com/kaeawc/pgmem-go/exec"
	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/storage"
	"github.com/kaeawc/pgmem-go/types"
)

func updateFixture(t *testing.T) (catalog.Schema, storage.Engine) {
	t.Helper()
	sch := catalog.NewSchema()
	eng := storage.NewEngine()
	if err := sch.CreateTable(catalog.Table{
		Name: "items",
		Columns: []catalog.Column{
			{Name: "id", Type: types.Int4, NotNull: true, Unique: true},
			{Name: "label", Type: types.Text},
			{Name: "qty", Type: types.Int4, NotNull: true},
		},
		Checks: []catalog.Check{{
			Name: "items_qty_check",
			Expr: &ir.BinOp{Op: ">", T: types.Bool, Left: &ir.ColumnRef{Name: "qty"}, Right: &ir.Literal{Value: int32(0), T: types.Int4}},
		}},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	eng.CreateTable("items", 3)
	st, _ := eng.Table("items")
	st.Insert(storage.Row{int32(1), "alpha", int32(10)})
	st.Insert(storage.Row{int32(2), "beta", int32(20)})
	st.Insert(storage.Row{int32(3), "gamma", int32(30)})
	return sch, eng
}

func runUpdatePlan(t *testing.T, sch catalog.Schema, eng storage.Engine, plan *ir.Update) ([]exec.Row, error) {
	t.Helper()
	txn, _ := eng.Begin(context.Background())
	defer txn.Rollback()
	op, err := exec.Build(plan, &exec.Env{Schema: sch, Engine: eng, Txn: txn})
	if err != nil {
		return nil, err
	}
	defer op.Close()
	var rows []exec.Row
	for {
		row, err := op.Next(context.Background())
		if errors.Is(err, io.EOF) {
			return rows, nil
		}
		if err != nil {
			return rows, err
		}
		rows = append(rows, row)
	}
}

func snapshotRows(t *testing.T, eng storage.Engine, table string) []storage.Row {
	t.Helper()
	st, _ := eng.Table(table)
	rows := st.Rows()
	sort.Slice(rows, func(i, j int) bool { return rows[i][0].(int32) < rows[j][0].(int32) })
	return rows
}

// TestUpdate_WhereMatchesOne updates the matching row and leaves the
// rest untouched.
func TestUpdate_WhereMatchesOne(t *testing.T) {
	sch, eng := updateFixture(t)
	plan := &ir.Update{
		Table: "items",
		Assignments: []ir.Assignment{
			{Column: "label", Expr: &ir.Literal{Value: "renamed", T: types.Text}},
		},
		Where: &ir.BinOp{
			Op: "=", T: types.Bool,
			Left: &ir.ColumnRef{Name: "id"}, Right: &ir.Literal{Value: int32(2), T: types.Int4},
		},
	}
	if _, err := runUpdatePlan(t, sch, eng, plan); err != nil {
		t.Fatalf("update: %v", err)
	}
	got := snapshotRows(t, eng, "items")
	want := []storage.Row{
		{int32(1), "alpha", int32(10)},
		{int32(2), "renamed", int32(20)},
		{int32(3), "gamma", int32(30)},
	}
	for i, r := range got {
		for j := range r {
			if r[j] != want[i][j] {
				t.Errorf("row %d col %d: got %v, want %v", i, j, r[j], want[i][j])
			}
		}
	}
}

// TestUpdate_AssignmentsSeeOriginal: PG semantics — every assignment
// evaluates against the original row, never against another
// assignment's freshly-written value. Swap id ↔ qty on row 1 (id=1,
// qty=10) and confirm the result is {10, ..., 1}, not {10, ..., 10}.
//
// Side note: this also dodges the items_qty_check (qty > 0) since the
// new qty is 1.
func TestUpdate_AssignmentsSeeOriginal(t *testing.T) {
	sch, eng := updateFixture(t)
	plan := &ir.Update{
		Table: "items",
		Assignments: []ir.Assignment{
			{Column: "id", Expr: &ir.ColumnRef{Name: "qty"}},
			{Column: "qty", Expr: &ir.ColumnRef{Name: "id"}},
		},
		Where: &ir.BinOp{
			Op: "=", T: types.Bool,
			Left: &ir.ColumnRef{Name: "id"}, Right: &ir.Literal{Value: int32(1), T: types.Int4},
		},
	}
	if _, err := runUpdatePlan(t, sch, eng, plan); err != nil {
		t.Fatalf("update: %v", err)
	}
	got := snapshotRows(t, eng, "items")
	// Sorted by id: {2,beta,20}, {3,gamma,30}, {10,alpha,1}. The swap
	// landed on the originally-id=1 row.
	swapped := got[len(got)-1]
	if swapped[0] != int32(10) || swapped[1] != "alpha" || swapped[2] != int32(1) {
		t.Errorf("after swap: got %v, want {10 alpha 1}", swapped)
	}
}

// TestUpdate_Returning emits the post-update row.
func TestUpdate_Returning(t *testing.T) {
	sch, eng := updateFixture(t)
	plan := &ir.Update{
		Table: "items",
		Assignments: []ir.Assignment{
			{Column: "label", Expr: &ir.Literal{Value: "X", T: types.Text}},
		},
		Where: &ir.BinOp{
			Op: ">", T: types.Bool,
			Left: &ir.ColumnRef{Name: "id"}, Right: &ir.Literal{Value: int32(1), T: types.Int4},
		},
		Returning:      []ir.Expr{&ir.ColumnRef{Name: "id"}, &ir.ColumnRef{Name: "label"}},
		ReturningNames: []string{"id", "label"},
	}
	rows, err := runUpdatePlan(t, sch, eng, plan)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("returning rows: got %d, want 2", len(rows))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i][0].(int32) < rows[j][0].(int32) })
	for i, r := range rows {
		if r[1] != "X" {
			t.Errorf("row %d label: got %v, want X", i, r[1])
		}
	}
}

// TestUpdate_NotNullViolation: writing NULL into a NotNull column
// rejects the entire UPDATE and leaves storage untouched.
func TestUpdate_NotNullViolation(t *testing.T) {
	sch, eng := updateFixture(t)
	plan := &ir.Update{
		Table: "items",
		Assignments: []ir.Assignment{
			{Column: "qty", Expr: &ir.Literal{Value: nil, T: nil}},
		},
		Where: &ir.BinOp{Op: "=", T: types.Bool,
			Left: &ir.ColumnRef{Name: "id"}, Right: &ir.Literal{Value: int32(2), T: types.Int4}},
	}
	_, err := runUpdatePlan(t, sch, eng, plan)
	var sqlErr *exec.SQLError
	if !errors.As(err, &sqlErr) || sqlErr.Code != "23502" {
		t.Errorf("err: got %v, want 23502", err)
	}
	// Check storage unchanged.
	got := snapshotRows(t, eng, "items")
	if len(got) != 3 || got[1][2] != int32(20) {
		t.Errorf("storage mutated after rejected update: %v", got)
	}
}

// TestUpdate_UniqueViolation: changing a UNIQUE column to a value that
// collides with another row rejects.
func TestUpdate_UniqueViolation(t *testing.T) {
	sch, eng := updateFixture(t)
	plan := &ir.Update{
		Table: "items",
		Assignments: []ir.Assignment{
			{Column: "id", Expr: &ir.Literal{Value: int32(1), T: types.Int4}},
		},
		Where: &ir.BinOp{Op: "=", T: types.Bool,
			Left: &ir.ColumnRef{Name: "id"}, Right: &ir.Literal{Value: int32(2), T: types.Int4}},
	}
	_, err := runUpdatePlan(t, sch, eng, plan)
	var sqlErr *exec.SQLError
	if !errors.As(err, &sqlErr) || sqlErr.Code != "23505" {
		t.Errorf("err: got %v, want 23505", err)
	}
	got := snapshotRows(t, eng, "items")
	if len(got) != 3 {
		t.Errorf("storage mutated: %v", got)
	}
}

// TestUpdate_CheckViolation: writing a value that fails CHECK rejects.
func TestUpdate_CheckViolation(t *testing.T) {
	sch, eng := updateFixture(t)
	plan := &ir.Update{
		Table: "items",
		Assignments: []ir.Assignment{
			{Column: "qty", Expr: &ir.Literal{Value: int32(0), T: types.Int4}},
		},
		Where: &ir.BinOp{Op: "=", T: types.Bool,
			Left: &ir.ColumnRef{Name: "id"}, Right: &ir.Literal{Value: int32(2), T: types.Int4}},
	}
	_, err := runUpdatePlan(t, sch, eng, plan)
	var sqlErr *exec.SQLError
	if !errors.As(err, &sqlErr) || sqlErr.Code != "23514" {
		t.Errorf("err: got %v, want 23514", err)
	}
	got := snapshotRows(t, eng, "items")
	if got[1][2] != int32(20) {
		t.Errorf("storage mutated: row 1 qty = %v", got[1][2])
	}
}

// TestUpdate_NoMatchLeavesIntact: WHERE that matches no rows is a
// successful no-op.
func TestUpdate_NoMatchLeavesIntact(t *testing.T) {
	sch, eng := updateFixture(t)
	plan := &ir.Update{
		Table: "items",
		Assignments: []ir.Assignment{
			{Column: "label", Expr: &ir.Literal{Value: "ignored", T: types.Text}},
		},
		Where: &ir.BinOp{Op: "=", T: types.Bool,
			Left: &ir.ColumnRef{Name: "id"}, Right: &ir.Literal{Value: int32(999), T: types.Int4}},
	}
	if _, err := runUpdatePlan(t, sch, eng, plan); err != nil {
		t.Fatalf("update: %v", err)
	}
	got := snapshotRows(t, eng, "items")
	want := []string{"alpha", "beta", "gamma"}
	for i, r := range got {
		if r[1] != want[i] {
			t.Errorf("row %d label: got %v, want %v", i, r[1], want[i])
		}
	}
}
