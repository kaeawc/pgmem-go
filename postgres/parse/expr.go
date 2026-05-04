package parse

import (
	"fmt"
	"strconv"
	"strings"

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
		// `op ANY (array)` is the SQL-standard "match any element"
		// form sqlc emits for list parameters. Parse it as a
		// dedicated AnyExpr so the eval path can iterate the array.
		if p.acceptIdent("any") {
			if _, err := p.expect(tLParen, "("); err != nil {
				return nil, err
			}
			arr, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(tRParen, ")"); err != nil {
				return nil, err
			}
			return &ir.AnyExpr{Probe: left, Op: op, Array: arr}, nil
		}
		right, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		return &ir.BinOp{Op: op, Left: left, Right: right, T: types.Bool}, nil
	}
	if p.peek().kind == kwIn {
		return p.parseInClause(left, false)
	}
	if p.peek().kind == kwNot && p.peekNext().kind == kwIn {
		p.consume() // NOT
		return p.parseInClause(left, true)
	}
	if p.peek().kind == kwIs {
		return p.parseIsNull(left)
	}
	if p.peek().kind == kwBetween {
		return p.parseBetween(left, false)
	}
	if p.peek().kind == kwNot && p.peekNext().kind == kwBetween {
		p.consume() // NOT
		return p.parseBetween(left, true)
	}
	if op, ok := likeOp(p.peek().kind); ok {
		p.consume()
		right, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		return &ir.BinOp{Op: op, Left: left, Right: right, T: types.Bool}, nil
	}
	if p.peek().kind == kwNot {
		if op, ok := likeOp(p.peekNext().kind); ok {
			p.consume() // NOT
			p.consume() // LIKE/ILIKE
			right, err := p.parseAdditive()
			if err != nil {
				return nil, err
			}
			return &ir.UnaryOp{
				Op:   "not",
				Expr: &ir.BinOp{Op: op, Left: left, Right: right, T: types.Bool},
				T:    types.Bool,
			}, nil
		}
	}
	return left, nil
}

// parsePosition consumes `( substr IN str )` and returns a strpos
// FuncCall — strpos's arg order is (haystack, needle) which is the
// reverse of position's (substr IN haystack), so we swap. Both
// operands parse at additive precedence so the inner IN keyword is
// consumed here, not by parseComparison's `expr IN (list)` path.
func (p *parser) parsePosition() (ir.Expr, error) {
	if _, err := p.expect(tLParen, "("); err != nil {
		return nil, err
	}
	substr, err := p.parseAdditive()
	if err != nil {
		return nil, err
	}
	if !p.accept(kwIn) {
		return nil, fmt.Errorf("parse: expected IN in POSITION at %d", p.peek().pos)
	}
	str, err := p.parseAdditive()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tRParen, ")"); err != nil {
		return nil, err
	}
	return &ir.FuncCall{Name: "strpos", Args: []ir.Expr{str, substr}}, nil
}

// parseExtract consumes `( field FROM expr )`. The opening EXTRACT
// has been consumed by the caller. We desugar to a `date_part`
// FuncCall whose first arg is the field name as a text literal.
func (p *parser) parseExtract() (ir.Expr, error) {
	if _, err := p.expect(tLParen, "("); err != nil {
		return nil, err
	}
	field, err := p.expect(tIdent, "EXTRACT field name")
	if err != nil {
		return nil, err
	}
	if !p.accept(kwFrom) {
		return nil, fmt.Errorf("parse: expected FROM in EXTRACT at %d", p.peek().pos)
	}
	source, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tRParen, ")"); err != nil {
		return nil, err
	}
	return &ir.FuncCall{
		Name: "date_part",
		Args: []ir.Expr{
			&ir.Literal{Value: strings.ToLower(field.val), T: types.Text},
			source,
		},
	}, nil
}

// parseExists consumes `EXISTS ( SELECT ... )`. The leading EXISTS
// has not been consumed yet.
func (p *parser) parseExists() (ir.Expr, error) {
	p.consume() // EXISTS
	if _, err := p.expect(tLParen, "("); err != nil {
		return nil, err
	}
	plan, err := p.parseSelect()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tRParen, ")"); err != nil {
		return nil, err
	}
	return &ir.ExistsExpr{Plan: plan}, nil
}

// parseCase consumes a `CASE [operand] WHEN ... THEN ... [ELSE ...] END`
// expression. Both forms share the same IR shape: Operand is nil for
// the searched form; for the simple form it's the value compared
// against each WHEN.
func (p *parser) parseCase() (ir.Expr, error) {
	p.consume() // CASE
	var operand ir.Expr
	if p.peek().kind != kwWhen {
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		operand = e
	}
	if p.peek().kind != kwWhen {
		return nil, fmt.Errorf("parse: expected WHEN in CASE at %d", p.peek().pos)
	}
	var whens []ir.CaseWhen
	for p.accept(kwWhen) {
		match, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if !p.accept(kwThen) {
			return nil, fmt.Errorf("parse: expected THEN in CASE at %d", p.peek().pos)
		}
		result, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		whens = append(whens, ir.CaseWhen{Match: match, Result: result})
	}
	var elseExpr ir.Expr
	if p.accept(kwElse) {
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		elseExpr = e
	}
	if !p.accept(kwEnd) {
		return nil, fmt.Errorf("parse: expected END to close CASE at %d", p.peek().pos)
	}
	return &ir.Case{Operand: operand, Whens: whens, Else: elseExpr}, nil
}

// parseBetween desugars `x BETWEEN a AND b` into `x >= a AND x <= b`,
// and `x NOT BETWEEN a AND b` into `x < a OR x > b`. The bounds are
// parsed at parseAdditive precedence so the inner AND is the keyword
// terminator, not a logical-AND combinator over them.
func (p *parser) parseBetween(probe ir.Expr, negate bool) (ir.Expr, error) {
	p.consume() // BETWEEN
	lo, err := p.parseAdditive()
	if err != nil {
		return nil, err
	}
	if !p.accept(kwAnd) {
		return nil, fmt.Errorf("parse: expected AND in BETWEEN at %d", p.peek().pos)
	}
	hi, err := p.parseAdditive()
	if err != nil {
		return nil, err
	}
	if negate {
		return &ir.BinOp{
			Op:    "or",
			Left:  &ir.BinOp{Op: "<", Left: probe, Right: lo, T: types.Bool},
			Right: &ir.BinOp{Op: ">", Left: probe, Right: hi, T: types.Bool},
			T:     types.Bool,
		}, nil
	}
	return &ir.BinOp{
		Op:    "and",
		Left:  &ir.BinOp{Op: ">=", Left: probe, Right: lo, T: types.Bool},
		Right: &ir.BinOp{Op: "<=", Left: probe, Right: hi, T: types.Bool},
		T:     types.Bool,
	}, nil
}

// parseIsNull consumes the `IS …` postfix after `left`. We support
// `IS NULL`, `IS NOT NULL`, `IS DISTINCT FROM expr`, and
// `IS NOT DISTINCT FROM expr`. The first two are unary ops whose
// evaluator skips the standard NULL short-circuit; the latter two
// are binary ops that treat NULL as a comparable value.
func (p *parser) parseIsNull(left ir.Expr) (ir.Expr, error) {
	p.consume() // IS
	negate := p.accept(kwNot)
	if p.accept(kwDistinct) {
		if !p.accept(kwFrom) {
			return nil, fmt.Errorf("parse: expected FROM after DISTINCT at %d", p.peek().pos)
		}
		right, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		op := "is distinct from"
		if negate {
			op = "is not distinct from"
		}
		return &ir.BinOp{Op: op, Left: left, Right: right, T: types.Bool}, nil
	}
	if !p.accept(kwNull) {
		return nil, fmt.Errorf("parse: expected NULL or DISTINCT after IS at %d", p.peek().pos)
	}
	op := "is null"
	if negate {
		op = "is not null"
	}
	return &ir.UnaryOp{Op: op, Expr: left, T: types.Bool}, nil
}

// isParenlessNow reports whether the identifier names one of PG's
// SQL-standard parenless datetime keywords. The lowercase name is what
// our builtin registry expects.
func isParenlessNow(name string) bool {
	switch strings.ToLower(name) {
	case "current_timestamp", "current_date", "current_time":
		return true
	}
	return false
}

func likeOp(k tokenKind) (string, bool) {
	switch k {
	case kwLike:
		return "like", true
	case kwIlike:
		return "ilike", true
	}
	return "", false
}

// peekNext returns the token after p.peek() without consuming. Used
// to confirm two-token leading combinations like NOT IN / NOT BETWEEN.
func (p *parser) peekNext() token {
	if p.pos+1 >= len(p.toks) {
		return p.toks[len(p.toks)-1]
	}
	return p.toks[p.pos+1]
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
	case tArrow:
		return "->", true
	case tArrowText:
		return "->>", true
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
	case tRegex:
		return "~", true
	case tRegexI:
		return "~*", true
	case tNRegex:
		return "!~", true
	case tNRegexI:
		return "!~*", true
	case tContains:
		return "@>", true
	case tContained:
		return "<@", true
	case tQuestion:
		return "?", true
	default:
		return "", false
	}
}

func (p *parser) parsePrimary() (ir.Expr, error) {
	base, err := p.parsePrimaryHead()
	if err != nil {
		return nil, err
	}
	// Postfix `::type` casts. They chain — `x::int::text` is fine.
	for p.peek().kind == tCast {
		p.consume()
		typeName, err := p.expect(tIdent, "cast target type")
		if err != nil {
			return nil, err
		}
		name := typeName.val
		// `double precision` and `character varying` are two-word
		// types in real PG. If the next token is also an ident and
		// "name word" forms a known type, consume it as part of the
		// name. Otherwise fall through with the single-word name.
		if p.peek().kind == tIdent {
			combined := name + " " + p.peek().val
			if _, ok := types.ByName(combined); ok {
				p.consume()
				name = combined
			}
		}
		if p.accept(tLBracket) {
			if !p.accept(tRBracket) {
				return nil, fmt.Errorf("parse: expected ']' after '[' at %d", p.peek().pos)
			}
			name += "[]"
		}
		t, ok := types.ByName(name)
		if !ok {
			return nil, fmt.Errorf("parse: unknown cast target type %q", name)
		}
		base = &ir.Cast{Expr: base, T: t}
	}
	return base, nil
}

// parsePrimaryHead parses a primary expression *without* the postfix
// cast handling. Lifted out so the cast loop can wrap whatever the
// head produces.
func (p *parser) parsePrimaryHead() (ir.Expr, error) {
	t := p.peek()
	switch t.kind {
	case tMinus:
		// Unary minus binds tighter than the additive minus parsed in
		// parseAdditive — `-a + b` is `(-a) + b`, not `-(a + b)`.
		p.consume()
		inner, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		return &ir.UnaryOp{Op: "-", Expr: inner, T: inner.Type()}, nil
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
	case kwCase:
		return p.parseCase()
	case kwExists:
		return p.parseExists()
	case tParam:
		p.consume()
		idx, err := strconv.Atoi(t.val)
		if err != nil || idx < 1 {
			return nil, fmt.Errorf("parse: bad parameter %q", t.val)
		}
		return &ir.ParamRef{Index: idx - 1}, nil
	case tIdent:
		// EXTRACT is a context keyword: `extract(field from expr)`. We
		// desugar to a `date_part(field-text, expr)` call so the rest
		// of the pipeline doesn't need a special node.
		if strings.EqualFold(t.val, "extract") && p.peekNext().kind == tLParen {
			p.consume()
			return p.parseExtract()
		}
		// `position(substr IN str)` is keyword-syntax sugar for the
		// strpos builtin with the operand order reversed.
		if strings.EqualFold(t.val, "position") && p.peekNext().kind == tLParen {
			p.consume() // POSITION
			return p.parsePosition()
		}
		// `interval 'N unit'` is the standard literal form. We rewrite
		// it to a `interval('N unit')` builtin call so downstream
		// stages don't need a special node.
		if strings.EqualFold(t.val, "interval") && p.peekNext().kind == tString {
			p.consume() // INTERVAL
			s := p.consume()
			return &ir.FuncCall{
				Name: "interval",
				Args: []ir.Expr{&ir.Literal{Value: s.val, T: types.Text}},
			}, nil
		}
		p.consume()
		// PG keeps a few SQL-standard datetime keywords as bare names:
		// `current_timestamp`, `current_date`, `current_time` are valid
		// expressions on their own. We desugar to a paren-less builtin
		// call so the rest of the pipeline doesn't need a special node.
		if isParenlessNow(t.val) && p.peek().kind != tLParen && p.peek().kind != tDot {
			return &ir.FuncCall{Name: strings.ToLower(t.val)}, nil
		}
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
// `(*)` is recognized for the aggregate count(*) shape — it produces a
// FuncCall with no args and a star marker the parser caller can spot
// via FuncCallIsStar.
//
// The function name has already been consumed by the caller.
func (p *parser) parseFuncCall(name string) (ir.Expr, error) {
	if _, err := p.expect(tLParen, "("); err != nil {
		return nil, err
	}
	if p.accept(tStar) {
		if _, err := p.expect(tRParen, ")"); err != nil {
			return nil, err
		}
		return &ir.FuncCall{Name: name, Args: nil, Star: true}, nil
	}
	distinct := p.accept(kwDistinct)
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
	fc := &ir.FuncCall{Name: name, Args: args, Distinct: distinct}
	if p.acceptIdent("over") {
		spec, err := p.parseWindowSpec()
		if err != nil {
			return nil, err
		}
		fc.Window = &spec
	}
	return fc, nil
}

// parseWindowSpec consumes `( [PARTITION BY expr, …] [ORDER BY key,
// …] )`. The OVER keyword has been consumed by the caller. Frame
// clauses (ROWS BETWEEN …) aren't modelled yet.
func (p *parser) parseWindowSpec() (ir.WindowSpec, error) {
	var spec ir.WindowSpec
	if _, err := p.expect(tLParen, "("); err != nil {
		return spec, err
	}
	if p.acceptIdent("partition") {
		if !p.accept(kwBy) {
			return spec, fmt.Errorf("parse: expected BY after PARTITION at %d", p.peek().pos)
		}
		for {
			e, err := p.parseExpr()
			if err != nil {
				return spec, err
			}
			spec.PartitionBy = append(spec.PartitionBy, e)
			if !p.accept(tComma) {
				break
			}
		}
	}
	if p.accept(kwOrder) {
		if _, err := p.expect(kwBy, "BY"); err != nil {
			return spec, err
		}
		keys, err := p.parseSortKeys()
		if err != nil {
			return spec, err
		}
		spec.OrderBy = keys
	}
	if _, err := p.expect(tRParen, ")"); err != nil {
		return spec, err
	}
	return spec, nil
}

// parseNumberLiteral picks int4 / int8 / float8 based on the token's
// shape. Tokens with a `.` or exponent land as float8; integers
// outside the int4 range widen to int8.
func (p *parser) parseNumberLiteral() (ir.Expr, error) {
	t := p.consume()
	if strings.ContainsAny(t.val, ".eE") {
		f, err := strconv.ParseFloat(t.val, 64)
		if err != nil {
			return nil, fmt.Errorf("parse: bad float %q: %w", t.val, err)
		}
		return &ir.Literal{Value: f, T: types.Float8}, nil
	}
	n, err := strconv.ParseInt(t.val, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse: bad integer %q: %w", t.val, err)
	}
	if n >= -1<<31 && n < 1<<31 {
		return &ir.Literal{Value: int32(n), T: types.Int4}, nil
	}
	return &ir.Literal{Value: n, T: types.Int8}, nil
}
