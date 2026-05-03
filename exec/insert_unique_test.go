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

// uniqueFixture sets up a (catalog, engine) pair around a single table
// with a configurable column unique-flag layout. Returned ready for an
// Insert plan to be built against it.
func uniqueFixture(t *testing.T, name string, cols []catalog.Column) (catalog.Schema, storage.Engine) {
	t.Helper()
	sch := catalog.NewSchema()
	eng := storage.NewEngine()
	if err := sch.CreateTable(catalog.Table{Name: name, Columns: cols}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	eng.CreateTable(name, len(cols))
	return sch, eng
}

func runInsert(t *testing.T, sch catalog.Schema, eng storage.Engine, plan *ir.Insert) error {
	t.Helper()
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
	// Side-effect operators (Insert, CreateTable) signal "done" with EOF
	// even on success — they have no rows to produce. Test helpers want
	// the first non-EOF error, or nil.
	if _, err := op.Next(context.Background()); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// TestInsert_UniqueViolation_AgainstExistingRow covers the simplest
// case: two INSERTs into a UNIQUE column with the same value. The
// second one must fail with SQLSTATE 23505.
func TestInsert_UniqueViolation_AgainstExistingRow(t *testing.T) {
	sch, eng := uniqueFixture(t, "users", []catalog.Column{
		{Name: "id", Type: types.Int4, NotNull: true, Unique: true},
		{Name: "name", Type: types.Text},
	})

	first := &ir.Insert{
		Table:   "users",
		Columns: []string{"id", "name"},
		Rows: [][]ir.Expr{{
			&ir.Literal{Value: int32(1), T: types.Int4},
			&ir.Literal{Value: "alice", T: types.Text},
		}},
	}
	if err := runInsert(t, sch, eng, first); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	second := &ir.Insert{
		Table:   "users",
		Columns: []string{"id", "name"},
		Rows: [][]ir.Expr{{
			&ir.Literal{Value: int32(1), T: types.Int4},
			&ir.Literal{Value: "alice-again", T: types.Text},
		}},
	}
	err := runInsert(t, sch, eng, second)
	if err == nil {
		t.Fatal("second insert: want UNIQUE error, got nil")
	}
	var sqlErr *exec.SQLError
	if !errors.As(err, &sqlErr) {
		t.Fatalf("err type: got %T (%v), want *exec.SQLError", err, err)
	}
	if sqlErr.Code != "23505" {
		t.Errorf("SQLState: got %q, want %q", sqlErr.Code, "23505")
	}
	if !strings.Contains(sqlErr.Message, `"users_id_key"`) {
		t.Errorf("Message: got %q, want it to name the implicit constraint users_id_key", sqlErr.Message)
	}

	st, _ := eng.Table("users")
	if got := len(st.Rows()); got != 1 {
		t.Errorf("rows after rejected dup: got %d, want 1", got)
	}
}

// TestInsert_UniqueViolation_WithinSameStatement is the case the
// "validate then insert" structure was built for: a multi-row INSERT
// that contains a duplicate within itself. Neither row may land.
func TestInsert_UniqueViolation_WithinSameStatement(t *testing.T) {
	sch, eng := uniqueFixture(t, "tags", []catalog.Column{
		{Name: "name", Type: types.Text, NotNull: true, Unique: true},
	})
	plan := &ir.Insert{
		Table:   "tags",
		Columns: []string{"name"},
		Rows: [][]ir.Expr{
			{&ir.Literal{Value: "go", T: types.Text}},
			{&ir.Literal{Value: "rust", T: types.Text}},
			{&ir.Literal{Value: "go", T: types.Text}}, // dup with row 0
		},
	}
	err := runInsert(t, sch, eng, plan)
	if err == nil {
		t.Fatal("want UNIQUE error, got nil")
	}
	var sqlErr *exec.SQLError
	if !errors.As(err, &sqlErr) || sqlErr.Code != "23505" {
		t.Fatalf("err: got %v, want SQLSTATE 23505", err)
	}
	st, _ := eng.Table("tags")
	if got := len(st.Rows()); got != 0 {
		t.Errorf("rows after rejected multi-row insert: got %d, want 0", got)
	}
}

// TestInsert_UniqueAllowsMultipleNulls matches PG's rule: NULL is not
// equal to anything for the purposes of UNIQUE, so a UNIQUE column
// without NOT NULL admits any number of NULL rows.
func TestInsert_UniqueAllowsMultipleNulls(t *testing.T) {
	sch, eng := uniqueFixture(t, "logs", []catalog.Column{
		{Name: "trace_id", Type: types.Text, Unique: true},
	})
	plan := &ir.Insert{
		Table:   "logs",
		Columns: []string{"trace_id"},
		Rows: [][]ir.Expr{
			{&ir.Literal{Value: nil, T: nil}},
			{&ir.Literal{Value: nil, T: nil}},
			{&ir.Literal{Value: "abc", T: types.Text}},
		},
	}
	if err := runInsert(t, sch, eng, plan); err != nil {
		t.Fatalf("insert: %v", err)
	}
	st, _ := eng.Table("logs")
	if got := len(st.Rows()); got != 3 {
		t.Errorf("rows: got %d, want 3", got)
	}
}

// TestInsert_PrimaryKey_IsNotNullAndUnique confirms that the catalog
// flags PK columns as both NotNull and Unique — the desugaring is the
// whole reason PRIMARY KEY exists in the parser before the rest of the
// constraint surface lands.
func TestInsert_PrimaryKey_IsNotNullAndUnique(t *testing.T) {
	sch, eng := uniqueFixture(t, "accounts", []catalog.Column{
		{Name: "id", Type: types.Int4, NotNull: true, Unique: true}, // simulates "id int PRIMARY KEY"
		{Name: "name", Type: types.Text},
	})
	if err := runInsert(t, sch, eng, &ir.Insert{
		Table:   "accounts",
		Columns: []string{"id", "name"},
		Rows: [][]ir.Expr{{
			&ir.Literal{Value: int32(1), T: types.Int4},
			&ir.Literal{Value: "alice", T: types.Text},
		}},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	dupID := runInsert(t, sch, eng, &ir.Insert{
		Table:   "accounts",
		Columns: []string{"id", "name"},
		Rows:    [][]ir.Expr{{&ir.Literal{Value: int32(1), T: types.Int4}, &ir.Literal{Value: "bob", T: types.Text}}},
	})
	var sqlErr *exec.SQLError
	if !errors.As(dupID, &sqlErr) || sqlErr.Code != "23505" {
		t.Errorf("dup-id: got %v, want 23505", dupID)
	}

	nullID := runInsert(t, sch, eng, &ir.Insert{
		Table:   "accounts",
		Columns: []string{"id", "name"},
		Rows:    [][]ir.Expr{{&ir.Literal{Value: nil, T: nil}, &ir.Literal{Value: "carol", T: types.Text}}},
	})
	if !errors.As(nullID, &sqlErr) || sqlErr.Code != "23502" {
		t.Errorf("null-id: got %v, want 23502", nullID)
	}
}
