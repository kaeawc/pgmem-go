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

func serialFixture(t *testing.T, autoType types.Type) (catalog.Schema, storage.Engine) {
	t.Helper()
	sch := catalog.NewSchema()
	eng := storage.NewEngine()
	if err := sch.CreateTable(catalog.Table{
		Name: "t",
		Columns: []catalog.Column{
			{Name: "id", Type: autoType, NotNull: true, Unique: true, Auto: true},
			{Name: "label", Type: types.Text, NotNull: true},
		},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	eng.CreateTable("t", 2)
	return sch, eng
}

func runInsertReturnRows(t *testing.T, sch catalog.Schema, eng storage.Engine, plan *ir.Insert) ([]exec.Row, error) {
	t.Helper()
	txn, _ := eng.Begin(context.Background())
	defer func() { _ = txn.Rollback() }()
	op, err := exec.Build(plan, &exec.Env{Schema: sch, Engine: eng, Txn: txn})
	if err != nil {
		return nil, err
	}
	defer op.Close()
	var rows []exec.Row
	for {
		row, err := op.Next(context.Background())
		if errors.Is(err, io.EOF) {
			return rows, txn.Commit()
		}
		if err != nil {
			return rows, err
		}
		rows = append(rows, row)
	}
}

// TestSerial_AssignsSequentialIDs: omitting the auto column from the
// INSERT triggers the engine fill. Three inserts should yield ids
// 1, 2, 3.
func TestSerial_AssignsSequentialIDs(t *testing.T) {
	sch, eng := serialFixture(t, types.Int4)
	for i, label := range []string{"a", "b", "c"} {
		plan := &ir.Insert{
			Table:          "t",
			Columns:        []string{"label"},
			Rows:           [][]ir.Expr{{&ir.Literal{Value: label, T: types.Text}}},
			Returning:      []ir.Expr{&ir.ColumnRef{Name: "id"}},
			ReturningNames: []string{"id"},
		}
		rows, err := runInsertReturnRows(t, sch, eng, plan)
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
		if len(rows) != 1 {
			t.Fatalf("returning rows: got %d, want 1", len(rows))
		}
		want := int32(i + 1)
		if rows[0][0] != want {
			t.Errorf("insert %d returned id %v, want %d", i, rows[0][0], want)
		}
	}
}

// TestBigSerial_ReturnsInt64 confirms BIGSERIAL is the int8 variant —
// the value lands as int64 in the row, not int32.
func TestBigSerial_ReturnsInt64(t *testing.T) {
	sch, eng := serialFixture(t, types.Int8)
	plan := &ir.Insert{
		Table:          "t",
		Columns:        []string{"label"},
		Rows:           [][]ir.Expr{{&ir.Literal{Value: "x", T: types.Text}}},
		Returning:      []ir.Expr{&ir.ColumnRef{Name: "id"}},
		ReturningNames: []string{"id"},
	}
	rows, err := runInsertReturnRows(t, sch, eng, plan)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, ok := rows[0][0].(int64); !ok {
		t.Errorf("returned id type: got %T, want int64", rows[0][0])
	}
}

// TestSerial_ExplicitValueOverridesCounter: providing an explicit value
// uses that value and *does not* advance the counter, matching PG's
// rule that the SERIAL sequence is independent of manual inserts.
func TestSerial_ExplicitValueOverridesCounter(t *testing.T) {
	sch, eng := serialFixture(t, types.Int4)
	// Manually insert id=42.
	manual := &ir.Insert{
		Table:   "t",
		Columns: []string{"id", "label"},
		Rows: [][]ir.Expr{{
			&ir.Literal{Value: int32(42), T: types.Int4},
			&ir.Literal{Value: "manual", T: types.Text},
		}},
	}
	if _, err := runInsertReturnRows(t, sch, eng, manual); err != nil {
		t.Fatalf("manual: %v", err)
	}
	// Now an auto-fill insert should still get id=1 (counter wasn't touched).
	auto := &ir.Insert{
		Table:          "t",
		Columns:        []string{"label"},
		Rows:           [][]ir.Expr{{&ir.Literal{Value: "auto", T: types.Text}}},
		Returning:      []ir.Expr{&ir.ColumnRef{Name: "id"}},
		ReturningNames: []string{"id"},
	}
	rows, err := runInsertReturnRows(t, sch, eng, auto)
	if err != nil {
		t.Fatalf("auto: %v", err)
	}
	if rows[0][0] != int32(1) {
		t.Errorf("auto id: got %v, want 1 (counter independent of manual)", rows[0][0])
	}
}

// TestSerial_RollbackLeavesGapInCounter: the canonical counter advances
// even for txns that don't commit. PG behaves the same way (sequence
// numbers are non-transactional).
func TestSerial_RollbackLeavesGapInCounter(t *testing.T) {
	sch, eng := serialFixture(t, types.Int4)

	// txn 1: take an id but roll back.
	tx, _ := eng.Begin(context.Background())
	plan := &ir.Insert{
		Table:   "t",
		Columns: []string{"label"},
		Rows:    [][]ir.Expr{{&ir.Literal{Value: "doomed", T: types.Text}}},
	}
	op, err := exec.Build(plan, &exec.Env{Schema: sch, Engine: eng, Txn: tx})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, err := op.Next(context.Background()); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("Next: %v", err)
	}
	op.Close()
	_ = tx.Rollback()

	// txn 2: should get id=2, not id=1 (the rolled-back txn consumed 1).
	plan2 := &ir.Insert{
		Table:          "t",
		Columns:        []string{"label"},
		Rows:           [][]ir.Expr{{&ir.Literal{Value: "kept", T: types.Text}}},
		Returning:      []ir.Expr{&ir.ColumnRef{Name: "id"}},
		ReturningNames: []string{"id"},
	}
	rows, err := runInsertReturnRows(t, sch, eng, plan2)
	if err != nil {
		t.Fatalf("plan2: %v", err)
	}
	if rows[0][0] != int32(2) {
		t.Errorf("post-rollback id: got %v, want 2 (gap from rollback)", rows[0][0])
	}
}
