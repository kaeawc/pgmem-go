package exec_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/kaeawc/pgmem-go/catalog"
	"github.com/kaeawc/pgmem-go/exec"
	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/storage"
	"github.com/kaeawc/pgmem-go/types"
)

func runInsertCheckFixture(t *testing.T, checkExpr ir.Expr, rows [][]ir.Expr) error {
	t.Helper()
	sch := catalog.NewSchema()
	eng := storage.NewEngine()
	if err := sch.CreateTable(catalog.Table{
		Name: "items",
		Columns: []catalog.Column{
			{Name: "id", Type: types.Int4, NotNull: true},
			{Name: "qty", Type: types.Int4, NotNull: true},
		},
		Checks: []catalog.Check{{Name: "items_qty_check", Expr: checkExpr}},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	eng.CreateTable("items", 2)

	plan := &ir.Insert{Table: "items", Columns: []string{"id", "qty"}, Rows: rows}
	txn, err := eng.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer txn.Rollback()

	op, err := exec.Build(plan, &exec.Env{Schema: sch, Engine: eng, Txn: txn})
	if err != nil {
		return err
	}
	defer op.Close()
	if _, err := op.Next(context.Background()); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// gtZero is the catalog form of `qty > 0` — the parser would emit this
// from `CHECK (qty > 0)`. ColumnRef.Name is set; the executor resolves
// .Index/.Type from the table schema at INSERT time.
func gtZero() ir.Expr {
	return &ir.BinOp{
		Op:    ">",
		Left:  &ir.ColumnRef{Name: "qty"},
		Right: &ir.Literal{Value: int32(0), T: types.Int4},
		T:     types.Bool,
	}
}

func TestInsert_CheckViolation_RejectsBadRow(t *testing.T) {
	err := runInsertCheckFixture(t, gtZero(), [][]ir.Expr{{
		&ir.Literal{Value: int32(1), T: types.Int4},
		&ir.Literal{Value: int32(0), T: types.Int4},
	}})
	if err == nil {
		t.Fatal("want CHECK error, got nil")
	}
	var sqlErr *exec.SQLError
	if !errors.As(err, &sqlErr) {
		t.Fatalf("err type: got %T (%v), want *exec.SQLError", err, err)
	}
	if sqlErr.Code != "23514" {
		t.Errorf("SQLState: got %q, want %q", sqlErr.Code, "23514")
	}
	if !strings.Contains(sqlErr.Message, `"items_qty_check"`) {
		t.Errorf("Message: got %q, want it to name items_qty_check", sqlErr.Message)
	}
}

func TestInsert_CheckPasses_AdmitsGoodRow(t *testing.T) {
	if err := runInsertCheckFixture(t, gtZero(), [][]ir.Expr{{
		&ir.Literal{Value: int32(1), T: types.Int4},
		&ir.Literal{Value: int32(5), T: types.Int4},
	}}); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

// TestInsert_CheckEvaluatesAgainstOtherColumn confirms the resolver
// pulls referenced column types from the table schema, not from the
// expression itself. The executor must wire ColumnRef.qty correctly
// even though the SQL author wrote it as a bare identifier.
func TestInsert_CheckEvaluatesAgainstOtherColumn(t *testing.T) {
	// CHECK (id < qty)
	expr := &ir.BinOp{
		Op:    "<",
		Left:  &ir.ColumnRef{Name: "id"},
		Right: &ir.ColumnRef{Name: "qty"},
		T:     types.Bool,
	}
	if err := runInsertCheckFixture(t, expr, [][]ir.Expr{{
		&ir.Literal{Value: int32(2), T: types.Int4},
		&ir.Literal{Value: int32(5), T: types.Int4},
	}}); err != nil {
		t.Fatalf("good row: %v", err)
	}

	err := runInsertCheckFixture(t, expr, [][]ir.Expr{{
		&ir.Literal{Value: int32(7), T: types.Int4},
		&ir.Literal{Value: int32(3), T: types.Int4},
	}})
	var sqlErr *exec.SQLError
	if !errors.As(err, &sqlErr) || sqlErr.Code != "23514" {
		t.Errorf("bad row: got %v, want 23514", err)
	}
}

// TestInsert_CheckNullEvaluatesAsPass matches PG's rule: a CHECK that
// evaluates to NULL is treated as success. Only an explicit FALSE
// rejects the row.
func TestInsert_CheckNullEvaluatesAsPass(t *testing.T) {
	// CHECK (qty > 0) with qty = NULL — comparison yields NULL, not FALSE.
	sch := catalog.NewSchema()
	eng := storage.NewEngine()
	if err := sch.CreateTable(catalog.Table{
		Name: "items",
		Columns: []catalog.Column{
			{Name: "id", Type: types.Int4, NotNull: true},
			{Name: "qty", Type: types.Int4}, // nullable
		},
		Checks: []catalog.Check{{Name: "items_qty_check", Expr: gtZero()}},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	eng.CreateTable("items", 2)

	plan := &ir.Insert{
		Table:   "items",
		Columns: []string{"id", "qty"},
		Rows: [][]ir.Expr{{
			&ir.Literal{Value: int32(1), T: types.Int4},
			&ir.Literal{Value: nil, T: nil},
		}},
	}
	txn, _ := eng.Begin(context.Background())
	defer txn.Rollback()
	op, err := exec.Build(plan, &exec.Env{Schema: sch, Engine: eng, Txn: txn})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer op.Close()
	if _, err := op.Next(context.Background()); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("Next: %v (NULL CHECK should pass)", err)
	}

	st, _ := eng.Table("items")
	if got := len(st.Rows()); got != 1 {
		t.Errorf("rows: got %d, want 1", got)
	}
}
