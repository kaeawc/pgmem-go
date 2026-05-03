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

// TestGenRandomUUID_ProducesV4 calls the builtin through the exec
// pipeline and checks the result is a [16]byte with the v4 version
// nibble set in the right position.
func TestGenRandomUUID_ProducesV4(t *testing.T) {
	sch := catalog.NewSchema()
	eng := storage.NewEngine()
	plan := &ir.Project{
		Input:       &ir.Values{Rows: [][]ir.Expr{{}}},
		Exprs:       []ir.Expr{&ir.FuncCall{Name: "gen_random_uuid"}},
		OutputNames: []string{"u"},
	}
	tx, _ := eng.Begin(context.Background())
	defer func() { _ = tx.Rollback() }()
	op, err := exec.Build(plan, &exec.Env{Schema: sch, Engine: eng, Txn: tx})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer op.Close()
	row, err := op.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	v, ok := row[0].([16]byte)
	if !ok {
		t.Fatalf("got %T, want [16]byte", row[0])
	}
	// v4 version: high nibble of byte 6 must be 0x4.
	if (v[6] >> 4) != 0x4 {
		t.Errorf("version nibble: got %x, want 4", v[6]>>4)
	}
	// RFC 4122 variant: top two bits of byte 8 must be 10.
	if (v[8] >> 6) != 0x2 {
		t.Errorf("variant bits: got %b, want 10", v[8]>>6)
	}
}

// TestGenRandomUUID_TwoCallsDiffer is a sanity check on randomness.
// crypto/rand collisions on 16 bytes are vanishingly rare.
func TestGenRandomUUID_TwoCallsDiffer(t *testing.T) {
	sch := catalog.NewSchema()
	eng := storage.NewEngine()

	makePlan := func() *ir.Project {
		return &ir.Project{
			Input:       &ir.Values{Rows: [][]ir.Expr{{}}},
			Exprs:       []ir.Expr{&ir.FuncCall{Name: "gen_random_uuid"}},
			OutputNames: []string{"u"},
		}
	}
	get := func() [16]byte {
		t.Helper()
		tx, _ := eng.Begin(context.Background())
		defer func() { _ = tx.Rollback() }()
		op, err := exec.Build(makePlan(), &exec.Env{Schema: sch, Engine: eng, Txn: tx})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		defer op.Close()
		row, err := op.Next(context.Background())
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		return row[0].([16]byte)
	}
	a, b := get(), get()
	if a == b {
		t.Errorf("two calls returned identical uuid %x", a)
	}
}

// TestUnknownFunctionErrors confirms calling a function that isn't in
// the registry surfaces a clear error at Build time, not at Eval time.
func TestUnknownFunctionErrors(t *testing.T) {
	sch := catalog.NewSchema()
	eng := storage.NewEngine()
	plan := &ir.Project{
		Input:       &ir.Values{Rows: [][]ir.Expr{{}}},
		Exprs:       []ir.Expr{&ir.FuncCall{Name: "no_such_function"}},
		OutputNames: []string{"x"},
	}
	tx, _ := eng.Begin(context.Background())
	defer func() { _ = tx.Rollback() }()
	if _, err := exec.Build(plan, &exec.Env{Schema: sch, Engine: eng, Txn: tx}); err == nil {
		t.Fatal("Build: want error, got nil")
	}
}

// TestUUIDColumn_RoundTripThroughInsert builds an INSERT plan with a
// uuid value and reads it back, exercising the type's encode path
// across the storage boundary.
func TestUUIDColumn_RoundTripThroughInsert(t *testing.T) {
	sch := catalog.NewSchema()
	eng := storage.NewEngine()
	if err := sch.CreateTable(catalog.Table{
		Name: "rows",
		Columns: []catalog.Column{
			{Name: "id", Type: types.UUID, NotNull: true, Unique: true},
			{Name: "label", Type: types.Text},
		},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	eng.CreateTable("rows", 2)

	want := [16]byte{0xde, 0xad, 0xbe, 0xef, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}
	plan := &ir.Insert{
		Table:   "rows",
		Columns: []string{"id", "label"},
		Rows: [][]ir.Expr{{
			&ir.Literal{Value: want, T: types.UUID},
			&ir.Literal{Value: "x", T: types.Text},
		}},
	}
	tx, _ := eng.Begin(context.Background())
	defer func() { _ = tx.Rollback() }()
	op, err := exec.Build(plan, &exec.Env{Schema: sch, Engine: eng, Txn: tx})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer op.Close()
	if _, err := op.Next(context.Background()); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("Next: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	st, _ := eng.Table("rows")
	rows := st.Rows()
	if len(rows) != 1 || rows[0][0] != want {
		t.Errorf("readback: got %v, want id=%v", rows, want)
	}
}
