package parse_test

import (
	"testing"

	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/postgres/parse"
)

// TestParse_ScalarSubqueryInSelectList confirms `(SELECT ...)` parses
// to ir.ScalarSubquery wherever a primary expression is allowed.
func TestParse_ScalarSubqueryInSelectList(t *testing.T) {
	node, err := parse.Parse(`SELECT (SELECT id FROM t WHERE k = 1) FROM other`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	proj := node.(*ir.Project)
	if _, ok := proj.Exprs[0].(*ir.ScalarSubquery); !ok {
		t.Errorf("Exprs[0]: got %T, want *ir.ScalarSubquery", proj.Exprs[0])
	}
}

// TestParse_InList: `IN (val, val, ...)` becomes ir.InListExpr.
func TestParse_InList(t *testing.T) {
	node, err := parse.Parse(`SELECT id FROM t WHERE id IN (1, 2, 3)`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cond := node.(*ir.Project).Input.(*ir.Filter).Cond
	in, ok := cond.(*ir.InListExpr)
	if !ok {
		t.Fatalf("Cond: got %T, want *ir.InListExpr", cond)
	}
	if len(in.List) != 3 {
		t.Errorf("List len: got %d, want 3", len(in.List))
	}
}

// TestParse_InSubquery: `IN (SELECT ...)` becomes ir.InSubqueryExpr.
func TestParse_InSubquery(t *testing.T) {
	node, err := parse.Parse(`SELECT id FROM t WHERE id IN (SELECT id FROM other)`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cond := node.(*ir.Project).Input.(*ir.Filter).Cond
	if _, ok := cond.(*ir.InSubqueryExpr); !ok {
		t.Errorf("Cond: got %T, want *ir.InSubqueryExpr", cond)
	}
}

// TestParse_NotIn: `NOT IN` parses to UnaryOp{not, InListExpr/...}.
func TestParse_NotIn(t *testing.T) {
	node, err := parse.Parse(`SELECT id FROM t WHERE id NOT IN (1, 2)`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cond := node.(*ir.Project).Input.(*ir.Filter).Cond
	u, ok := cond.(*ir.UnaryOp)
	if !ok || u.Op != "not" {
		t.Fatalf("Cond: got %T (%v), want UnaryOp{not}", cond, cond)
	}
	if _, ok := u.Expr.(*ir.InListExpr); !ok {
		t.Errorf("inner: got %T, want *ir.InListExpr", u.Expr)
	}
}
