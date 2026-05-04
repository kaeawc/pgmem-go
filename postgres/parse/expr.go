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
//   = != < > <= >=     (comparison; chained → no, single)
//   IN / NOT IN        (handled inside comparison)
//   + -                (additive)
//   * / %              (multiplicative)
//   primary            (literal, ident, $N, parenthesized, func call)
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
	if p.accept(kwNot) {
		inner, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &ir.UnaryOp{Op: "not", Expr: inner, T: types.Bool}, nil
	}
	return p.parseComparison()
}

func (p *parser) parseComparison() (ir.Expr, error) {
	left, err := p.parseAdditive()
	if err != nil {
		return nil, err
	}
	if op, ok := comparisonOp(p.peek().kind); ok {
		p.consume()
		right, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		return &ir.BinOp{Op: op, Left: left, Right: right, T: types.Bool}, nil
	}
	if p.peek().kind == kwIn {
		return p.parseInClause(left, false)
	}
	if p.peek().kind == kwNot && p.lookahead(1).kind == kwIn {
		p.consume() // NOT
		return p.parseInClause(left, true)
	}
	return left, nil
}

// lookahead peeks N tokens ahead without consuming. Used to confirm
// the NOT IN bigram before committing.
func (p *parser) lookahead(n int) token {
	if p.pos+n >= len(p.toks) {
		return p.toks[len(p.toks)-1]
	}
	return p.toks[p.pos+n]
}

// parseAdditive: left-associative + and -. Result type matches the
// wider operand (int4 + int8 → int8); resolved at exec.Build.
func (p *parser) parseAdditive() (ir.Expr, error) {
	left, err := p.parseMultiplicative()
	if err != nil {
		return nil, err
	}
	for {
		op, ok := additiveOp(p.peek().kind)
		if !ok {
			return left, nil
		}
		p.consume()
		right, err := p.parseMultiplicative()
		if err != nil {
			return nil, err
		}
		left = &ir.BinOp{Op: op, Left: left, Right: right}
	}
}

func additiveOp(k tokenKind) (string, bool) {
	switch k {
	case tPlus:
		return "+", true
	case tMinus:
		return "-", true
	case tConcat:
		return "||", true
	}
	return "", false
}

// parseMultiplicative: left-associative *, /, %. Same precedence
// family.
func (p *parser) parseMultiplicative() (ir.Expr, error) {
	left, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for {
		op, ok := multiplicativeOp(p.peek().kind)
		if !ok {
			return left, nil
		}
		p.consume()
		right, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		left = &ir.BinOp{Op: op, Left: left, Right: right}
	}
}

func multiplicativeOp(k tokenKind) (string, bool) {
	switch k {
	case tStar:
		return "*", true
	case tSlash:
		return "/", true
	case tPercent:
		return "%", true
	}
	return "", false
}

// parseInClause consumes IN ( <list-or-subquery> ). negate wraps the
// result in NOT for `expr NOT IN (...)`.
func (p *parser) parseInClause(probe ir.Expr, negate bool) (ir.Expr, error) {
	if !p.accept(kwIn) {
		return nil, fmt.Errorf("parse: expected IN at %d", p.peek().pos)
	}
	if _, err := p.expect(tLParen, "("); err != nil {
		return nil, err
	}
	var node ir.Expr
	if p.peek().kind == kwSelect {
		plan, err := p.parseSelect()
		if err != nil {
			return nil, err
		}
		node = &ir.InSubqueryExpr{Probe: probe, Plan: plan}
	} else {
		var list []ir.Expr
		for {
			e, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			list = append(list, e)
			if p.accept(tComma) {
				continue
			}
			break
		}
		node = &ir.InListExpr{Probe: probe, List: list}
	}
	if _, err := p.expect(tRParen, ")"); err != nil {
		return nil, err
	}
	if negate {
		return &ir.UnaryOp{Op: "not", Expr: node, T: types.Bool}, nil
	}
	return node, nil
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
		// `ident(args)` is a function call. `ident.ident` is a qualified
		// column reference. Bare identifiers stay as unqualified column
		// refs. We commit to each shape only after the disambiguating
		// token shows up.
		switch p.peek().kind {
		case tLParen:
			return p.parseFuncCall(t.val)
		case tDot:
			p.consume() // .
			col, err := p.expect(tIdent, "qualified column name")
			if err != nil {
				return nil, err
			}
			return &ir.ColumnRef{Qualifier: t.val, Name: col.val}, nil
		}
		return &ir.ColumnRef{Name: t.val}, nil
	default:
		return nil, fmt.Errorf("parse: unexpected token %q in expression", t.val)
	}
}

func (p *parser) parseParenExpr() (ir.Expr, error) {
	p.consume() // (
	// `(SELECT ...)` is a scalar subquery primary; we commit only if
	// we actually see SELECT after the paren so plain parenthesized
	// expressions still work.
	if p.peek().kind == kwSelect {
		plan, err := p.parseSelect()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tRParen, ")"); err != nil {
			return nil, err
		}
		return &ir.ScalarSubquery{Plan: plan}, nil
	}
	e, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tRParen, ")"); err != nil {
		return nil, err
	}
	return e, nil
}

// parseFuncCall consumes `(arg [, arg ...])`. Empty arg list is fine.
// The function name has already been consumed by the caller.
func (p *parser) parseFuncCall(name string) (ir.Expr, error) {
	if _, err := p.expect(tLParen, "("); err != nil {
		return nil, err
	}
	var args []ir.Expr
	if p.peek().kind != tRParen {
		for {
			a, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			args = append(args, a)
			if p.accept(tComma) {
				continue
			}
			break
		}
	}
	if _, err := p.expect(tRParen, ")"); err != nil {
		return nil, err
	}
	return &ir.FuncCall{Name: name, Args: args}, nil
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
