package parse

import (
	"fmt"

	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/types"
)

// parser is a hand-rolled recursive-descent parser for the M2 grammar.
// It is intentionally small: lex once, walk a token slice with one
// position cursor. No backtracking, no error recovery — failures abort
// the whole statement.
type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek() token { return p.toks[p.pos] }

func (p *parser) consume() token {
	t := p.toks[p.pos]
	p.pos++
	return t
}

func (p *parser) accept(k tokenKind) bool {
	if p.peek().kind == k {
		p.pos++
		return true
	}
	return false
}

func (p *parser) expect(k tokenKind, ctx string) (token, error) {
	t := p.peek()
	if t.kind != k {
		return token{}, fmt.Errorf("parse: expected %s, got %q (pos %d)", ctx, t.val, t.pos)
	}
	p.pos++
	return t, nil
}

// Statement entry point.

func (p *parser) parseStmt() (ir.Node, error) {
	tok := p.peek()
	switch tok.kind {
	case kwSelect:
		return p.parseSelect()
	case kwInsert:
		return p.parseInsert()
	case kwCreate:
		return p.parseCreateTable()
	default:
		return nil, fmt.Errorf("parse: unsupported leading token %q", tok.val)
	}
}

// --- CREATE TABLE ---

func (p *parser) parseCreateTable() (ir.Node, error) {
	p.consume() // CREATE
	if _, err := p.expect(kwTable, "TABLE"); err != nil {
		return nil, err
	}
	name, err := p.expect(tIdent, "table name")
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tLParen, "("); err != nil {
		return nil, err
	}
	var cols []ir.ColumnDef
	for {
		col, err := p.parseColumnDef()
		if err != nil {
			return nil, err
		}
		cols = append(cols, col)
		if p.accept(tComma) {
			continue
		}
		break
	}
	if _, err := p.expect(tRParen, ")"); err != nil {
		return nil, err
	}
	return &ir.CreateTable{Name: name.val, Columns: cols}, nil
}

func (p *parser) parseColumnDef() (ir.ColumnDef, error) {
	name, err := p.expect(tIdent, "column name")
	if err != nil {
		return ir.ColumnDef{}, err
	}
	typeName, err := p.expect(tIdent, "column type")
	if err != nil {
		return ir.ColumnDef{}, err
	}
	// VARCHAR(N): consume and discard the length.
	if p.accept(tLParen) {
		if _, err := p.expect(tNumber, "type length"); err != nil {
			return ir.ColumnDef{}, err
		}
		if _, err := p.expect(tRParen, ")"); err != nil {
			return ir.ColumnDef{}, err
		}
	}
	t, ok := types.ByName(typeName.val)
	if !ok {
		return ir.ColumnDef{}, fmt.Errorf("parse: unknown type %q", typeName.val)
	}
	def := ir.ColumnDef{Name: name.val, Type: t}
	for {
		done, err := p.parseColumnConstraint(&def)
		if err != nil {
			return ir.ColumnDef{}, err
		}
		if done {
			return def, nil
		}
	}
}

// parseColumnConstraint consumes one column constraint (NOT NULL, NULL,
// UNIQUE, PRIMARY KEY) and updates def. Returns done=true when no more
// constraints follow.
func (p *parser) parseColumnConstraint(def *ir.ColumnDef) (done bool, err error) {
	switch {
	case p.accept(kwNot):
		if _, err := p.expect(kwNull, "NULL"); err != nil {
			return false, err
		}
		def.NotNull = true
	case p.accept(kwNull):
		// Explicit NULL is the default; nothing to set.
	case p.accept(kwUnique):
		def.Unique = true
	case p.accept(kwPrimary):
		if _, err := p.expect(kwKey, "KEY"); err != nil {
			return false, err
		}
		def.NotNull = true
		def.Unique = true
	case p.accept(kwCheck):
		expr, err := p.parseParenExpr()
		if err != nil {
			return false, err
		}
		def.Check = expr
	default:
		return true, nil
	}
	return false, nil
}

// --- INSERT ---

func (p *parser) parseInsert() (ir.Node, error) {
	p.consume() // INSERT
	if _, err := p.expect(kwInto, "INTO"); err != nil {
		return nil, err
	}
	name, err := p.expect(tIdent, "table name")
	if err != nil {
		return nil, err
	}
	stmt := &ir.Insert{Table: name.val}
	if p.accept(tLParen) {
		for {
			col, err := p.expect(tIdent, "column name")
			if err != nil {
				return nil, err
			}
			stmt.Columns = append(stmt.Columns, col.val)
			if p.accept(tComma) {
				continue
			}
			break
		}
		if _, err := p.expect(tRParen, ")"); err != nil {
			return nil, err
		}
	}
	if _, err := p.expect(kwValues, "VALUES"); err != nil {
		return nil, err
	}
	for {
		row, err := p.parseValuesTuple()
		if err != nil {
			return nil, err
		}
		stmt.Rows = append(stmt.Rows, row)
		if p.accept(tComma) {
			continue
		}
		break
	}
	if p.accept(kwReturning) {
		exprs, names, err := p.parseSelectList()
		if err != nil {
			return nil, err
		}
		stmt.Returning = exprs
		stmt.ReturningNames = names
	}
	return stmt, nil
}

func (p *parser) parseValuesTuple() ([]ir.Expr, error) {
	if _, err := p.expect(tLParen, "("); err != nil {
		return nil, err
	}
	var out []ir.Expr
	for {
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		out = append(out, e)
		if p.accept(tComma) {
			continue
		}
		break
	}
	if _, err := p.expect(tRParen, ")"); err != nil {
		return nil, err
	}
	return out, nil
}

// --- SELECT ---

func (p *parser) parseSelect() (ir.Node, error) {
	p.consume() // SELECT
	exprs, names, err := p.parseSelectList()
	if err != nil {
		return nil, err
	}

	var input ir.Node = &ir.Values{Rows: [][]ir.Expr{{}}}
	if p.accept(kwFrom) {
		t, err := p.expect(tIdent, "table name")
		if err != nil {
			return nil, err
		}
		input = &ir.Scan{Table: t.val}
	}
	if p.accept(kwWhere) {
		cond, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		input = &ir.Filter{Input: input, Cond: cond}
	}

	plan := ir.Node(&ir.Project{Input: input, Exprs: exprs, OutputNames: names})

	if p.accept(kwOrder) {
		if _, err := p.expect(kwBy, "BY"); err != nil {
			return nil, err
		}
		keys, err := p.parseSortKeys()
		if err != nil {
			return nil, err
		}
		plan = &ir.Sort{Input: plan, Keys: keys}
	}

	if hasLimitOrOffset(p) {
		plan, err = p.parseLimitOffset(plan)
		if err != nil {
			return nil, err
		}
	}

	return plan, nil
}

func (p *parser) parseSelectList() ([]ir.Expr, []string, error) {
	var exprs []ir.Expr
	var names []string
	for {
		e, name, err := p.parseSelectItem()
		if err != nil {
			return nil, nil, err
		}
		exprs = append(exprs, e)
		names = append(names, name)
		if p.accept(tComma) {
			continue
		}
		break
	}
	return exprs, names, nil
}

func (p *parser) parseSelectItem() (ir.Expr, string, error) {
	e, err := p.parseExpr()
	if err != nil {
		return nil, "", err
	}
	name := defaultColName(e)
	if p.accept(kwAs) {
		t, err := p.expect(tIdent, "alias")
		if err != nil {
			return nil, "", err
		}
		name = t.val
	} else if p.peek().kind == tIdent {
		// Implicit alias: SELECT col alias FROM ...
		t := p.consume()
		name = t.val
	}
	return e, name, nil
}

func defaultColName(e ir.Expr) string {
	if c, ok := e.(*ir.ColumnRef); ok {
		return c.Name
	}
	return "?column?"
}

func (p *parser) parseSortKeys() ([]ir.SortKey, error) {
	var out []ir.SortKey
	for {
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		desc := false
		if p.accept(kwDesc) {
			desc = true
		} else if p.accept(kwAsc) {
			desc = false
		}
		out = append(out, ir.SortKey{Expr: e, Desc: desc})
		if p.accept(tComma) {
			continue
		}
		break
	}
	return out, nil
}

func hasLimitOrOffset(p *parser) bool {
	k := p.peek().kind
	return k == kwLimit || k == kwOffset
}

func (p *parser) parseLimitOffset(plan ir.Node) (ir.Node, error) {
	var count, offset ir.Expr
	for hasLimitOrOffset(p) {
		switch p.consume().kind {
		case kwLimit:
			e, err := p.parsePrimary()
			if err != nil {
				return nil, err
			}
			count = e
		case kwOffset:
			e, err := p.parsePrimary()
			if err != nil {
				return nil, err
			}
			offset = e
		}
	}
	return &ir.Limit{Input: plan, Count: count, Offset: offset}, nil
}
