package exec_test

import (
	"sort"
	"testing"

	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/types"
)

// TestLeftJoin_PadsUnmatched: every left row appears at least once.
// Carol has no orders in the fixture, so she still appears with NULL
// for orders.label.
func TestLeftJoin_PadsUnmatched(t *testing.T) {
	sch, eng := joinFixture(t)
	plan := &ir.Project{
		Input: &ir.Join{
			Type:  ir.JoinLeft,
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
	type pair struct {
		name  string
		label any
	}
	got := make([]pair, len(rows))
	for i, r := range rows {
		got[i] = pair{r[0].(string), r[1]}
	}
	sort.Slice(got, func(i, j int) bool {
		if got[i].name != got[j].name {
			return got[i].name < got[j].name
		}
		return labelLess(got[i].label, got[j].label)
	})
	want := []pair{
		{"alice", "alice-order-A"},
		{"alice", "alice-order-B"},
		{"bob", "bob-order"},
		{"carol", nil},
	}
	if len(got) != len(want) {
		t.Fatalf("rows: got %v, want %v", got, want)
	}
	for i := range got {
		if got[i].name != want[i].name || got[i].label != want[i].label {
			t.Errorf("row %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// labelLess sorts strings before nil so carol lands at the end after
// alice/bob in the test above.
func labelLess(a, b any) bool {
	if a == nil {
		return false
	}
	if b == nil {
		return true
	}
	return a.(string) < b.(string)
}

// TestCrossJoin_Cartesian emits every (left, right) pair without
// filtering. With 3 users and 3 orders we expect 9 output rows.
func TestCrossJoin_Cartesian(t *testing.T) {
	sch, eng := joinFixture(t)
	plan := &ir.Project{
		Input: &ir.Join{
			Type:  ir.JoinCross,
			Left:  &ir.Scan{Table: "users"},
			Right: &ir.Scan{Table: "orders"},
		},
		Exprs:       []ir.Expr{&ir.ColumnRef{Qualifier: "users", Name: "id"}},
		OutputNames: []string{"id"},
	}
	rows, _, err := runReadPlan(t, sch, eng, plan)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(rows) != 9 {
		t.Errorf("rows: got %d, want 9", len(rows))
	}
}
