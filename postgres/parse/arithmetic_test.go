package parse_test

import (
	"testing"

	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/postgres/parse"
)

// TestParse_Arithmetic_Precedence: `a + b * c` should produce
// (a + (b * c)), not ((a + b) * c).
func TestParse_Arithmetic_Precedence(t *testing.T) {
	node, err := parse.Parse(`SELECT a + b * c FROM t`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	expr := node.(*ir.Project).Exprs[0].(*ir.BinOp)
	if expr.Op != "+" {
		t.Errorf("outer op: got %q, want +", expr.Op)
	}
	right, ok := expr.Right.(*ir.BinOp)
	if !ok || right.Op != "*" {
		t.Fatalf("right side: got %T (%v), want *ir.BinOp{*}", expr.Right, expr.Right)
	}
}

// TestParse_Arithmetic_LeftAssociativity: `a - b - c` is ((a-b)-c).
func TestParse_Arithmetic_LeftAssociativity(t *testing.T) {
	node, err := parse.Parse(`SELECT a - b - c FROM t`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	outer := node.(*ir.Project).Exprs[0].(*ir.BinOp)
	if outer.Op != "-" {
		t.Errorf("outer op: got %q, want -", outer.Op)
	}
	left, ok := outer.Left.(*ir.BinOp)
	if !ok || left.Op != "-" {
		t.Fatalf("left side: got %T (%v), want *ir.BinOp{-}", outer.Left, outer.Left)
	}
}
