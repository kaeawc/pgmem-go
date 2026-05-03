package parse

import (
	"fmt"
	"strconv"

	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/types"
)

// Expression precedence, low → high:
//   OR
//   AND
//   NOT       (unary)
//   = != < > <= >=
//   primary   (literal, ident, $N, parenthesized)
//
// A precedence-climbing parser would be more compact, but this layout
// makes it obvious which operators bind tighter when something looks
// off.

func (p *parser) parseExpr() (ir.Expr, error) { return p.parseOr() }

func (p *parser) parseOr() (ir.Expr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == kwOr {
		p.consume()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &ir.BinOp{Op: "or", Left: left, Right: right, T: types.Bool}
	}
	return left, nil
}

func (p *parser) parseAnd() (ir.Expr, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == kwAnd {
		p.consume()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = &ir.BinOp{Op: "and", Left: left, Right: right, T: types.Bool}
	}
	return left, nil
}

func (p *parser) parseNot() (ir.Expr, error) {
	if _, ok := p.accept(kwNot); ok {
		inner, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &ir.UnaryOp{Op: "not", Expr: inner, T: types.Bool}, nil
	}
	return p.parseComparison()
}

func (p *parser) parseComparison() (ir.Expr, error) {
	left, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	op, ok := comparisonOp(p.peek().kind)
	if !ok {
		return left, nil
	}
	p.consume()
	right, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	return &ir.BinOp{Op: op, Left: left, Right: right, T: types.Bool}, nil
}

func comparisonOp(k tokenKind) (string, bool) {
	switch k {
	case tEq:
		return "=", true
	case tNeq:
		return "!=", true
	case tLt:
		return "<", true
	case tGt:
		return ">", true
	case tLte:
		return "<=", true
	case tGte:
		return ">=", true
	default:
		return "", false
	}
}

func (p *parser) parsePrimary() (ir.Expr, error) {
	t := p.peek()
	switch t.kind {
	case tLParen:
		return p.parseParenExpr()
	case tNumber:
		return p.parseNumberLiteral()
	case tString:
		p.consume()
		return &ir.Literal{Value: t.val, T: types.Text}, nil
	case kwTrue:
		p.consume()
		return &ir.Literal{Value: true, T: types.Bool}, nil
	case kwFalse:
		p.consume()
		return &ir.Literal{Value: false, T: types.Bool}, nil
	case kwNull:
		p.consume()
		return &ir.Literal{Value: nil, T: nil}, nil
	case tParam:
		p.consume()
		idx, err := strconv.Atoi(t.val)
		if err != nil || idx < 1 {
			return nil, fmt.Errorf("parse: bad parameter %q", t.val)
		}
		return &ir.ParamRef{Index: idx - 1}, nil
	case tIdent:
		p.consume()
		return &ir.ColumnRef{Name: t.val}, nil
	default:
		return nil, fmt.Errorf("parse: unexpected token %q in expression", t.val)
	}
}

func (p *parser) parseParenExpr() (ir.Expr, error) {
	p.consume() // (
	e, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tRParen, ")"); err != nil {
		return nil, err
	}
	return e, nil
}

// parseNumberLiteral picks int4 if the value fits, otherwise int8.
// Floating-point literals land with the numeric type in M5.
func (p *parser) parseNumberLiteral() (ir.Expr, error) {
	t := p.consume()
	n, err := strconv.ParseInt(t.val, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse: bad integer %q: %w", t.val, err)
	}
	if n >= -1<<31 && n < 1<<31 {
		return &ir.Literal{Value: int32(n), T: types.Int4}, nil
	}
	return &ir.Literal{Value: n, T: types.Int8}, nil
}
