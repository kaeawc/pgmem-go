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

// TestInsert_NotNullViolation_ReturnsSQLState checks that the executor
// surfaces NOT NULL violations as exec.SQLError with SQLSTATE 23502 and
// the column / table named in the message — that's what pgx-style
// clients pattern-match on.
//
// Uses the real (in-memory) catalog and storage rather than a fake; the
// "fakes" called for in MILESTONES.md M3 land when we add the snapshot
// tx layer. For NOT NULL the real types are tiny enough that adding a
// fake would obscure the test more than it'd help.
func TestInsert_NotNullViolation_ReturnsSQLState(t *testing.T) {
	sch := catalog.NewSchema()
	eng := storage.NewEngine()
	if err := sch.CreateTable(catalog.Table{
		Name: "items",
		Columns: []catalog.Column{
			{Name: "id", Type: types.Int4, NotNull: true},
			{Name: "label", Type: types.Text, NotNull: true},
		},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	eng.CreateTable("items", 2)

	plan := &ir.Insert{
		Table:   "items",
		Columns: []string{"id", "label"},
		Rows: [][]ir.Expr{{
			&ir.Literal{Value: int32(1), T: types.Int4},
			&ir.Literal{Value: nil, T: nil}, // SQL NULL into a NOT NULL col
		}},
	}

	txn, err := eng.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer txn.Rollback()

	op, err := exec.Build(plan, &exec.Env{Schema: sch, Engine: eng, Txn: txn})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer op.Close()

	_, err = op.Next(context.Background())
	if err == nil {
		t.Fatal("Next: want error, got nil")
	}
	var sqlErr *exec.SQLError
	if !errors.As(err, &sqlErr) {
		t.Fatalf("Next: want *exec.SQLError, got %T (%v)", err, err)
	}
	if sqlErr.Code != "23502" {
		t.Errorf("SQLState: got %q, want %q", sqlErr.Code, "23502")
	}
	const wantSubstr = `null value in column "label" of relation "items"`
	if !strings.Contains(sqlErr.Message, wantSubstr) {
		t.Errorf("Message: got %q, want substring %q", sqlErr.Message, wantSubstr)
	}

	// And nothing should have been inserted — the operator must not
	// half-apply a row when one of its columns fails the constraint.
	st, _ := eng.Table("items")
	if got := len(st.Rows()); got != 0 {
		t.Errorf("rows after failed insert: got %d, want 0", got)
	}
}

// TestInsert_NotNull_AllowsNullableColumn confirms we don't over-fire
// — only columns marked NotNull should reject NULL.
func TestInsert_NotNull_AllowsNullableColumn(t *testing.T) {
	sch := catalog.NewSchema()
	eng := storage.NewEngine()
	if err := sch.CreateTable(catalog.Table{
		Name: "notes",
		Columns: []catalog.Column{
			{Name: "id", Type: types.Int4, NotNull: true},
			{Name: "body", Type: types.Text, NotNull: false},
		},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	eng.CreateTable("notes", 2)

	plan := &ir.Insert{
		Table:   "notes",
		Columns: []string{"id", "body"},
		Rows: [][]ir.Expr{{
			&ir.Literal{Value: int32(7), T: types.Int4},
			&ir.Literal{Value: nil, T: nil},
		}},
	}

	txn, err := eng.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer txn.Rollback()

	op, err := exec.Build(plan, &exec.Env{Schema: sch, Engine: eng, Txn: txn})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer op.Close()

	if _, err := op.Next(context.Background()); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("Next: %v", err)
	}

	st, _ := eng.Table("notes")
	rows := st.Rows()
	if len(rows) != 1 {
		t.Fatalf("rows: got %d, want 1", len(rows))
	}
	if rows[0][0] != int32(7) || rows[0][1] != nil {
		t.Errorf("row: got %+v, want [7 <nil>]", rows[0])
	}
}

// TestInsert_MultiRow_NotNullValidatedBeforeAnyInsert checks that a
// failure on a later row in the VALUES list doesn't leave earlier rows
// committed. Real PG transactions roll the whole INSERT back; pgmem-go
// won't have transactions until later in M3, so this op-level guarantee
// stands in until then.
func TestInsert_MultiRow_NotNullValidatedBeforeAnyInsert(t *testing.T) {
	sch := catalog.NewSchema()
	eng := storage.NewEngine()
	if err := sch.CreateTable(catalog.Table{
		Name: "items",
		Columns: []catalog.Column{
			{Name: "id", Type: types.Int4, NotNull: true},
			{Name: "label", Type: types.Text, NotNull: true},
		},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	eng.CreateTable("items", 2)

	plan := &ir.Insert{
		Table:   "items",
		Columns: []string{"id", "label"},
		Rows: [][]ir.Expr{
			{&ir.Literal{Value: int32(1), T: types.Int4}, &ir.Literal{Value: "ok", T: types.Text}},
			{&ir.Literal{Value: int32(2), T: types.Int4}, &ir.Literal{Value: nil, T: nil}}, // bad
			{&ir.Literal{Value: int32(3), T: types.Int4}, &ir.Literal{Value: "also-ok", T: types.Text}},
		},
	}

	txn, err := eng.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer txn.Rollback()

	op, err := exec.Build(plan, &exec.Env{Schema: sch, Engine: eng, Txn: txn})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer op.Close()

	if _, err := op.Next(context.Background()); err == nil {
		t.Fatal("Next: want NOT NULL error, got nil")
	}

	st, _ := eng.Table("items")
	if got := len(st.Rows()); got != 0 {
		t.Errorf("rows after rejected multi-row insert: got %d, want 0", got)
	}
}
