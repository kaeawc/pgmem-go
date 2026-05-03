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

func deleteFixture(t *testing.T) (catalog.Schema, storage.Engine) {
	t.Helper()
	sch := catalog.NewSchema()
	eng := storage.NewEngine()
	if err := sch.CreateTable(catalog.Table{
		Name: "items",
		Columns: []catalog.Column{
			{Name: "id", Type: types.Int4, NotNull: true, Unique: true},
			{Name: "label", Type: types.Text},
		},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	eng.CreateTable("items", 2)
	st, _ := eng.Table("items")
	st.Insert(storage.Row{int32(1), "alpha"})
	st.Insert(storage.Row{int32(2), "beta"})
	st.Insert(storage.Row{int32(3), "gamma"})
	return sch, eng
}

func runPlan(t *testing.T, sch catalog.Schema, eng storage.Engine, plan ir.Node) ([]exec.Row, error) {
	t.Helper()
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

func remainingIDs(t *testing.T, eng storage.Engine) []int32 {
	t.Helper()
	st, _ := eng.Table("items")
	rows := st.Rows()
	ids := make([]int32, len(rows))
	for i, r := range rows {
		ids[i] = r[0].(int32)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// TestDelete_WhereMatchOne removes the matching row only.
func TestDelete_WhereMatchOne(t *testing.T) {
	sch, eng := deleteFixture(t)
	plan := &ir.Delete{
		Table: "items",
		Where: &ir.BinOp{
			Op: "=", T: types.Bool,
			Left: &ir.ColumnRef{Name: "id"}, Right: &ir.Literal{Value: int32(2), T: types.Int4},
		},
	}
	if _, err := runPlan(t, sch, eng, plan); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got := remainingIDs(t, eng)
	want := []int32{1, 3}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("remaining ids: got %v, want %v", got, want)
	}
}

// TestDelete_NoWhereDeletesAll mirrors PG's behavior: no WHERE means
// every row goes.
func TestDelete_NoWhereDeletesAll(t *testing.T) {
	sch, eng := deleteFixture(t)
	if _, err := runPlan(t, sch, eng, &ir.Delete{Table: "items"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := remainingIDs(t, eng); len(got) != 0 {
		t.Errorf("remaining: got %v, want empty", got)
	}
}

// TestDelete_Returning yields one projected row per deleted row.
func TestDelete_Returning(t *testing.T) {
	sch, eng := deleteFixture(t)
	plan := &ir.Delete{
		Table: "items",
		Where: &ir.BinOp{
			Op: ">", T: types.Bool,
			Left: &ir.ColumnRef{Name: "id"}, Right: &ir.Literal{Value: int32(1), T: types.Int4},
		},
		Returning:      []ir.Expr{&ir.ColumnRef{Name: "id"}, &ir.ColumnRef{Name: "label"}},
		ReturningNames: []string{"id", "label"},
	}
	rows, err := runPlan(t, sch, eng, plan)
	if err != nil {
		t.Fatalf("delete returning: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("returning rows: got %d, want 2", len(rows))
	}
	// Sort by id so the test doesn't depend on table iteration order.
	sort.Slice(rows, func(i, j int) bool { return rows[i][0].(int32) < rows[j][0].(int32) })
	if rows[0][0] != int32(2) || rows[0][1] != "beta" {
		t.Errorf("row 0: got %+v, want [2 beta]", rows[0])
	}
	if rows[1][0] != int32(3) || rows[1][1] != "gamma" {
		t.Errorf("row 1: got %+v, want [3 gamma]", rows[1])
	}
	if got := remainingIDs(t, eng); len(got) != 1 || got[0] != 1 {
		t.Errorf("remaining: got %v, want [1]", got)
	}
}

// TestDelete_NoMatchLeavesTableIntact handles the where-but-no-match
// case — important because some operators short-circuit on empty.
func TestDelete_NoMatchLeavesTableIntact(t *testing.T) {
	sch, eng := deleteFixture(t)
	plan := &ir.Delete{
		Table: "items",
		Where: &ir.BinOp{
			Op: "=", T: types.Bool,
			Left: &ir.ColumnRef{Name: "id"}, Right: &ir.Literal{Value: int32(999), T: types.Int4},
		},
	}
	if _, err := runPlan(t, sch, eng, plan); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := remainingIDs(t, eng); len(got) != 3 {
		t.Errorf("remaining: got %v, want 3 rows", got)
	}
}
