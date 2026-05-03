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

// joinFixture sets up two related tables — a parent (users) and a
// child (orders) referencing parent.id.
func joinFixture(t *testing.T) (catalog.Schema, storage.Engine) {
	t.Helper()
	sch := catalog.NewSchema()
	eng := storage.NewEngine()

	if err := sch.CreateTable(catalog.Table{
		Name: "users",
		Columns: []catalog.Column{
			{Name: "id", Type: types.Int4, NotNull: true, Unique: true},
			{Name: "name", Type: types.Text, NotNull: true},
		},
	}); err != nil {
		t.Fatalf("CreateTable users: %v", err)
	}
	eng.CreateTable("users", 2)
	u, _ := eng.Table("users")
	u.Insert(storage.Row{int32(1), "alice"})
	u.Insert(storage.Row{int32(2), "bob"})
	u.Insert(storage.Row{int32(3), "carol"})

	if err := sch.CreateTable(catalog.Table{
		Name: "orders",
		Columns: []catalog.Column{
			{Name: "id", Type: types.Int4, NotNull: true, Unique: true},
			{Name: "user_id", Type: types.Int4, NotNull: true},
			{Name: "label", Type: types.Text},
		},
	}); err != nil {
		t.Fatalf("CreateTable orders: %v", err)
	}
	eng.CreateTable("orders", 3)
	o, _ := eng.Table("orders")
	o.Insert(storage.Row{int32(100), int32(1), "alice-order-A"})
	o.Insert(storage.Row{int32(101), int32(1), "alice-order-B"})
	o.Insert(storage.Row{int32(102), int32(2), "bob-order"})
	// id=3 (carol) deliberately has no orders.
	return sch, eng
}

func runReadPlan(t *testing.T, sch catalog.Schema, eng storage.Engine, plan ir.Node) ([][]any, []exec.Column, error) {
	t.Helper()
	txn, _ := eng.Begin(context.Background())
	defer txn.Rollback()
	op, err := exec.Build(plan, &exec.Env{Schema: sch, Engine: eng, Txn: txn})
	if err != nil {
		return nil, nil, err
	}
	defer op.Close()
	schema := op.OutputSchema()
	var rows [][]any
	for {
		row, err := op.Next(context.Background())
		if errors.Is(err, io.EOF) {
			return rows, schema, nil
		}
		if err != nil {
			return rows, schema, err
		}
		rows = append(rows, []any(row))
	}
}

// TestInnerJoin_NestedLoop exercises the full path: SELECT users.name,
// orders.label FROM users JOIN orders ON users.id = orders.user_id.
func TestInnerJoin_NestedLoop(t *testing.T) {
	sch, eng := joinFixture(t)
	plan := &ir.Project{
		Input: &ir.Join{
			Type:  ir.JoinInner,
			Left:  &ir.Scan{Table: "users"},
			Right: &ir.Scan{Table: "orders"},
			Cond: &ir.BinOp{
				Op: "=", T: types.Bool,
				Left:  &ir.ColumnRef{Qualifier: "users", Name: "id"},
				Right: &ir.ColumnRef{Qualifier: "orders", Name: "user_id"},
			},
		},
		Exprs: []ir.Expr{
			&ir.ColumnRef{Qualifier: "users", Name: "name"},
			&ir.ColumnRef{Qualifier: "orders", Name: "label"},
		},
		OutputNames: []string{"name", "label"},
	}
	rows, _, err := runReadPlan(t, sch, eng, plan)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// Carol has no orders, so we expect 3 rows: 2 alice + 1 bob.
	type pair struct{ name, label string }
	got := make([]pair, len(rows))
	for i, r := range rows {
		got[i] = pair{r[0].(string), r[1].(string)}
	}
	sort.Slice(got, func(i, j int) bool {
		if got[i].name != got[j].name {
			return got[i].name < got[j].name
		}
		return got[i].label < got[j].label
	})
	want := []pair{
		{"alice", "alice-order-A"},
		{"alice", "alice-order-B"},
		{"bob", "bob-order"},
	}
	if len(got) != len(want) {
		t.Fatalf("rows: got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("row %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestInnerJoin_AmbiguousUnqualifiedRefIsRejected: when both sides of
// a join expose the same column, an unqualified `id` is ambiguous.
// Real PG returns 42702; we surface a clear error.
func TestInnerJoin_AmbiguousUnqualifiedRefIsRejected(t *testing.T) {
	sch, eng := joinFixture(t)
	plan := &ir.Project{
		Input: &ir.Join{
			Type:  ir.JoinInner,
			Left:  &ir.Scan{Table: "users"},
			Right: &ir.Scan{Table: "orders"},
			Cond: &ir.BinOp{
				Op: "=", T: types.Bool,
				Left:  &ir.ColumnRef{Qualifier: "users", Name: "id"},
				Right: &ir.ColumnRef{Qualifier: "orders", Name: "user_id"},
			},
		},
		Exprs:       []ir.Expr{&ir.ColumnRef{Name: "id"}}, // bare "id" — both sides have one
		OutputNames: []string{"id"},
	}
	if _, _, err := runReadPlan(t, sch, eng, plan); err == nil {
		t.Fatal("want ambiguity error, got nil")
	}
}

// TestInnerJoin_QualifierResolvesUnambiguously confirms that the same
// shape works when the bare ref is replaced with a qualified one.
func TestInnerJoin_QualifierResolvesUnambiguously(t *testing.T) {
	sch, eng := joinFixture(t)
	plan := &ir.Project{
		Input: &ir.Join{
			Type:  ir.JoinInner,
			Left:  &ir.Scan{Table: "users"},
			Right: &ir.Scan{Table: "orders"},
			Cond: &ir.BinOp{
				Op: "=", T: types.Bool,
				Left:  &ir.ColumnRef{Qualifier: "users", Name: "id"},
				Right: &ir.ColumnRef{Qualifier: "orders", Name: "user_id"},
			},
		},
		Exprs:       []ir.Expr{&ir.ColumnRef{Qualifier: "orders", Name: "id"}},
		OutputNames: []string{"order_id"},
	}
	rows, _, err := runReadPlan(t, sch, eng, plan)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("rows: got %d, want 3", len(rows))
	}
}

// TestInnerJoin_NoMatchEmits emits zero rows when no condition matches.
func TestInnerJoin_NoMatchEmits(t *testing.T) {
	sch, eng := joinFixture(t)
	plan := &ir.Project{
		Input: &ir.Join{
			Type:  ir.JoinInner,
			Left:  &ir.Scan{Table: "users"},
			Right: &ir.Scan{Table: "orders"},
			Cond: &ir.BinOp{
				Op: "=", T: types.Bool,
				Left:  &ir.ColumnRef{Qualifier: "users", Name: "id"},
				Right: &ir.Literal{Value: int32(999), T: types.Int4},
			},
		},
		Exprs:       []ir.Expr{&ir.ColumnRef{Qualifier: "users", Name: "name"}},
		OutputNames: []string{"name"},
	}
	rows, _, err := runReadPlan(t, sch, eng, plan)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows: got %d, want 0", len(rows))
	}
}
