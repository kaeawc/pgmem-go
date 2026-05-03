package parse_test

import (
	"testing"

	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/postgres/parse"
)

// TestParse_Join_BuildsLeftDeepTree confirms the parser models JOINs as
// a left-deep IR tree and that INNER is optional.
func TestParse_Join_BuildsLeftDeepTree(t *testing.T) {
	cases := []struct {
		name string
		sql  string
	}{
		{name: "join", sql: `SELECT a.id FROM a JOIN b ON a.id = b.a_id`},
		{name: "inner join", sql: `SELECT a.id FROM a INNER JOIN b ON a.id = b.a_id`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node, err := parse.Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			proj, ok := node.(*ir.Project)
			if !ok {
				t.Fatalf("plan: got %T, want *ir.Project", node)
			}
			j, ok := proj.Input.(*ir.Join)
			if !ok {
				t.Fatalf("Project.Input: got %T, want *ir.Join", proj.Input)
			}
			if j.Type != ir.JoinInner {
				t.Errorf("Type: got %v, want JoinInner", j.Type)
			}
			if _, ok := j.Left.(*ir.Scan); !ok {
				t.Errorf("Left: got %T, want *ir.Scan", j.Left)
			}
			if _, ok := j.Right.(*ir.Scan); !ok {
				t.Errorf("Right: got %T, want *ir.Scan", j.Right)
			}
			if j.Cond == nil {
				t.Error("Cond: got nil, want BinOp")
			}
		})
	}
}

// TestParse_QualifiedColumnRef confirms `table.col` parses to the right
// IR shape.
func TestParse_QualifiedColumnRef(t *testing.T) {
	node, err := parse.Parse(`SELECT a.id FROM a`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	proj := node.(*ir.Project)
	cr := proj.Exprs[0].(*ir.ColumnRef)
	if cr.Qualifier != "a" || cr.Name != "id" {
		t.Errorf("ColumnRef: got %q.%q, want a.id", cr.Qualifier, cr.Name)
	}
}

// TestParse_ChainedJoins handles multiple JOINs in one SELECT — the
// parser should produce a left-deep tree (((a JOIN b) JOIN c)).
func TestParse_ChainedJoins(t *testing.T) {
	node, err := parse.Parse(`SELECT a.id FROM a JOIN b ON a.id = b.a_id JOIN c ON b.id = c.b_id`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	outer := node.(*ir.Project).Input.(*ir.Join)
	// Outer right side is c.
	right := outer.Right.(*ir.Scan)
	if right.Table != "c" {
		t.Errorf("outer right: got %q, want c", right.Table)
	}
	// Outer left side is itself a Join (a JOIN b).
	inner := outer.Left.(*ir.Join)
	if inner.Left.(*ir.Scan).Table != "a" || inner.Right.(*ir.Scan).Table != "b" {
		t.Errorf("inner join shape: got %+v", inner)
	}
}
