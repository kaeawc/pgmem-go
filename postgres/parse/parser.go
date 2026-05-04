package parse

import (
	"fmt"
	"strings"

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
	// ctes maps a Common Table Expression name (lower-case) to its
	// plan. Entries are added by parseWith and consulted by
	// parseTableRef when it sees a name that isn't a real catalog
	// table. CTE scope is statement-wide for now: nested SELECTs see
	// the outer WITH's bindings, which matches PG for non-recursive
	// CTEs in the common case.
	ctes map[string]ir.Node
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

// acceptIdent consumes the next token if it is an identifier whose
// value matches name (case-insensitive). Used for context-keywords
// like NULLS / FIRST / LAST that PG keeps non-reserved so they can
// still be used as column names.
func (p *parser) acceptIdent(name string) bool {
	t := p.peek()
	if t.kind != tIdent {
		return false
	}
	if !strings.EqualFold(t.val, name) {
		return false
	}
	p.pos++
	return true
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
	if tok.kind == kwWith {
		return p.parseWith()
	}
	switch tok.kind {
	case kwSelect:
		return p.parseSelectMaybeUnion()
	case kwInsert:
		return p.parseInsert()
	case kwDelete:
		return p.parseDelete()
	case kwUpdate:
		return p.parseUpdate()
	case kwCreate:
		return p.parseCreateTable()
	case kwDrop:
		return p.parseDropTable()
	case kwTruncate:
		return p.parseTruncate()
	default:
		return nil, fmt.Errorf("parse: unsupported leading token %q", tok.val)
	}
}

// parseFromClause reads `FROM a [<type>] JOIN b [ON cond] [JOIN ...]`
// and produces a left-deep IR tree. JOIN type prefixes recognized:
// (none) / INNER / LEFT [OUTER] / CROSS. We don't model commas in FROM
// yet — JOINs only.
func (p *parser) parseFromClause() (ir.Node, error) {
	left, err := p.parseTableRef()
	if err != nil {
		return nil, err
	}
	for {
		if !p.peekIsJoin() {
			return left, nil
		}
		j, err := p.parseJoinSuffix(left)
		if err != nil {
			return nil, err
		}
		left = j
	}
}

// parseJoinSuffix consumes one `[<type>] JOIN b [ON cond]` clause and
// returns the join node sitting on top of the previous left side.
func (p *parser) parseJoinSuffix(left ir.Node) (ir.Node, error) {
	joinType := ir.JoinInner
	switch {
	case p.accept(kwInner):
		joinType = ir.JoinInner
	case p.accept(kwLeft):
		p.accept(kwOuter) // optional, no behavioural difference
		joinType = ir.JoinLeft
	case p.accept(kwCross):
		joinType = ir.JoinCross
	}
	if !p.accept(kwJoin) {
		return nil, fmt.Errorf("parse: expected JOIN at %d", p.peek().pos)
	}
	right, err := p.parseTableRef()
	if err != nil {
		return nil, err
	}
	var cond ir.Expr
	if joinType == ir.JoinCross {
		// CROSS JOIN forbids ON in standard SQL. Anything else is a parse
		// error here so users don't accidentally write a partial CROSS
		// JOIN with a condition we'd silently ignore.
		if p.peek().kind == kwOn {
			return nil, fmt.Errorf("parse: CROSS JOIN may not have ON clause (pos %d)", p.peek().pos)
		}
	} else {
		if _, err := p.expect(kwOn, "ON"); err != nil {
			return nil, err
		}
		cond, err = p.parseExpr()
		if err != nil {
			return nil, err
		}
	}
	return &ir.Join{Left: left, Right: right, Cond: cond, Type: joinType}, nil
}

func (p *parser) parseTableRef() (ir.Node, error) {
	if p.peek().kind == tLParen && p.peekNext().kind == kwSelect {
		return p.parseDerivedTable()
	}
	t, err := p.expect(tIdent, "table name")
	if err != nil {
		return nil, err
	}
	alias := p.parseOptionalAlias()
	if plan, ok := p.ctes[strings.ToLower(t.val)]; ok {
		// Aliases on CTE references aren't propagated yet — column refs
		// still work via name match against the inner plan's schema.
		_ = alias
		return plan, nil
	}
	return &ir.Scan{Table: t.val, Alias: alias}, nil
}

// parseDerivedTable consumes `( SELECT ... ) [AS] alias`. Real PG
// requires the alias on a FROM-subquery; we follow that.
func (p *parser) parseDerivedTable() (ir.Node, error) {
	p.consume() // (
	plan, err := p.parseSelectMaybeUnion()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tRParen, ")"); err != nil {
		return nil, err
	}
	alias := p.parseOptionalAlias()
	if alias == "" {
		return nil, fmt.Errorf("parse: subquery in FROM must have an alias at %d", p.peek().pos)
	}
	return &ir.SubqueryAlias{Inner: plan, Alias: alias}, nil
}

// parseOptionalAlias accepts `[AS] alias` if the next token can serve
// as a table alias. Returns "" when nothing alias-shaped follows. We
// don't gate on `kwAs` so the optional-AS form (`FROM users u`) works
// alongside the explicit form (`FROM users AS u`).
func (p *parser) parseOptionalAlias() string {
	if p.accept(kwAs) {
		t, err := p.expect(tIdent, "alias")
		if err != nil {
			return ""
		}
		return t.val
	}
	if p.peek().kind == tIdent && !aliasStopword(p.peek().val) {
		return p.consume().val
	}
	return ""
}

// aliasStopword reports whether a bare identifier following a table
// reference should be left for the next clause rather than consumed
// as an alias. PG's grammar makes ON / WHERE etc. proper keywords;
// our lexer keeps a few clause openers as identifiers (CTEs use ON
// internally, etc.), but in practice the keywords are reserved. The
// stopword list here is empty for now; extend it if a plain ident
// keyword starts to look ambiguous.
func aliasStopword(_ string) bool { return false }

// peekIsJoin reports whether the next token starts a JOIN clause —
// either `JOIN` directly or one of the prefix keywords.
func (p *parser) peekIsJoin() bool {
	switch p.peek().kind {
	case kwJoin, kwInner, kwLeft, kwCross:
		return true
	default:
		return false
	}
}

// --- UPDATE ---

func (p *parser) parseUpdate() (ir.Node, error) {
	p.consume() // UPDATE
	name, err := p.expect(tIdent, "table name")
	if err != nil {
		return nil, err
	}
	stmt := &ir.Update{Table: name.val}
	if _, err := p.expect(kwSet, "SET"); err != nil {
		return nil, err
	}
	for {
		col, err := p.expect(tIdent, "column name")
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tEq, "="); err != nil {
			return nil, err
		}
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		stmt.Assignments = append(stmt.Assignments, ir.Assignment{Column: col.val, Expr: e})
		if p.accept(tComma) {
			continue
		}
		break
	}
	if p.accept(kwWhere) {
		cond, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		stmt.Where = cond
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

// --- DELETE ---

func (p *parser) parseDelete() (ir.Node, error) {
	p.consume() // DELETE
	if _, err := p.expect(kwFrom, "FROM"); err != nil {
		return nil, err
	}
	name, err := p.expect(tIdent, "table name")
	if err != nil {
		return nil, err
	}
	stmt := &ir.Delete{Table: name.val}
	if p.accept(kwWhere) {
		cond, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		stmt.Where = cond
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

// --- DROP TABLE ---

// parseTruncate consumes
// `TRUNCATE [TABLE] name [, name ...] [RESTART IDENTITY] [CONTINUE
// IDENTITY] [CASCADE | RESTRICT]`. Trailing options are accepted but
// don't change behaviour.
func (p *parser) parseTruncate() (ir.Node, error) {
	p.consume() // TRUNCATE
	p.accept(kwTable)
	stmt := &ir.Truncate{}
	for {
		name, err := p.expect(tIdent, "table name")
		if err != nil {
			return nil, err
		}
		stmt.Tables = append(stmt.Tables, name.val)
		if !p.accept(tComma) {
			break
		}
	}
	for {
		switch {
		case p.acceptIdent("restart"):
			if !p.acceptIdent("identity") {
				return nil, fmt.Errorf("parse: expected IDENTITY after RESTART at %d", p.peek().pos)
			}
			stmt.RestartIdentity = true
		case p.acceptIdent("continue"):
			if !p.acceptIdent("identity") {
				return nil, fmt.Errorf("parse: expected IDENTITY after CONTINUE at %d", p.peek().pos)
			}
		case p.accept(kwCascade):
		case p.acceptIdent("restrict"):
		default:
			return stmt, nil
		}
	}
}

func (p *parser) parseDropTable() (ir.Node, error) {
	p.consume() // DROP
	if _, err := p.expect(kwTable, "TABLE"); err != nil {
		return nil, err
	}
	ifExists := false
	if p.accept(kwIf) {
		if _, err := p.expect(kwExists, "EXISTS"); err != nil {
			return nil, err
		}
		ifExists = true
	}
	name, err := p.expect(tIdent, "table name")
	if err != nil {
		return nil, err
	}
	return &ir.DropTable{Name: name.val, IfExists: ifExists}, nil
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
	def := ir.ColumnDef{Name: name.val}
	if t, auto, ok := resolveSerial(typeName.val); ok {
		// SERIAL / BIGSERIAL: Postgres desugars these to (int4|int8) +
		// NOT NULL + DEFAULT nextval(...). We squash that into Auto +
		// NotNull on the catalog column.
		def.Type = t
		def.Auto = auto
		def.NotNull = true
	} else {
		t, ok := types.ByName(typeName.val)
		if !ok {
			return ir.ColumnDef{}, fmt.Errorf("parse: unknown type %q", typeName.val)
		}
		def.Type = t
	}
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

// resolveSerial recognizes the SERIAL / BIGSERIAL pseudo-types and
// returns the underlying integer type plus the Auto flag so the
// caller can flatten them into a regular ColumnDef.
func resolveSerial(name string) (types.Type, bool, bool) {
	switch name {
	case "serial":
		return types.Int4, true, true
	case "bigserial":
		return types.Int8, true, true
	default:
		return nil, false, false
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
	case p.accept(kwReferences):
		ref, err := p.parseReferencesClause()
		if err != nil {
			return false, err
		}
		def.References = ref
	default:
		return true, nil
	}
	return false, nil
}

// parseReferencesClause consumes `<table>(<col>) [ON DELETE <action>]`.
// The REFERENCES keyword has already been consumed by the caller.
func (p *parser) parseReferencesClause() (*ir.ColumnRefSpec, error) {
	tbl, err := p.expect(tIdent, "referenced table")
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tLParen, "("); err != nil {
		return nil, err
	}
	col, err := p.expect(tIdent, "referenced column")
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tRParen, ")"); err != nil {
		return nil, err
	}
	spec := &ir.ColumnRefSpec{Table: tbl.val, Column: col.val}
	if p.peek().kind == kwOn && p.peekNext().kind == kwDelete {
		p.consume() // ON
		p.consume() // DELETE
		action, err := p.parseOnDeleteAction()
		if err != nil {
			return nil, err
		}
		spec.OnDelete = action
	}
	return spec, nil
}

// parseOnDeleteAction reads CASCADE / SET NULL / RESTRICT (NO ACTION
// is treated as the default and not parsed in this slice).
func (p *parser) parseOnDeleteAction() (ir.OnDeleteAction, error) {
	switch {
	case p.accept(kwCascade):
		return ir.OnDeleteCascade, nil
	case p.accept(kwSet):
		if _, err := p.expect(kwNull, "NULL"); err != nil {
			return 0, err
		}
		return ir.OnDeleteSetNull, nil
	default:
		return 0, fmt.Errorf("parse: expected CASCADE or SET NULL after ON DELETE at %d", p.peek().pos)
	}
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
	if p.peek().kind == kwSelect {
		src, err := p.parseSelectMaybeUnion()
		if err != nil {
			return nil, err
		}
		stmt.Source = src
	} else {
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
	}
	if p.peek().kind == kwOn {
		oc, err := p.parseOnConflict()
		if err != nil {
			return nil, err
		}
		stmt.OnConflict = oc
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

// parseOnConflict consumes `ON CONFLICT (col[, col]) DO NOTHING`. The
// leading ON has not been consumed. CONFLICT, DO, NOTHING are matched
// as context idents so they remain usable as column names.
func (p *parser) parseOnConflict() (*ir.OnConflict, error) {
	p.consume() // ON
	if !p.acceptIdent("conflict") {
		return nil, fmt.Errorf("parse: expected CONFLICT after ON at %d", p.peek().pos)
	}
	if _, err := p.expect(tLParen, "("); err != nil {
		return nil, err
	}
	var cols []string
	for {
		c, err := p.expect(tIdent, "conflict-target column")
		if err != nil {
			return nil, err
		}
		cols = append(cols, c.val)
		if p.accept(tComma) {
			continue
		}
		break
	}
	if _, err := p.expect(tRParen, ")"); err != nil {
		return nil, err
	}
	if !p.acceptIdent("do") {
		return nil, fmt.Errorf("parse: expected DO at %d", p.peek().pos)
	}
	if p.acceptIdent("nothing") {
		return &ir.OnConflict{Columns: cols, DoNothing: true}, nil
	}
	if p.accept(kwUpdate) {
		if _, err := p.expect(kwSet, "SET"); err != nil {
			return nil, err
		}
		assigns, err := p.parseAssignmentList()
		if err != nil {
			return nil, err
		}
		return &ir.OnConflict{Columns: cols, DoUpdate: assigns}, nil
	}
	return nil, fmt.Errorf("parse: expected NOTHING or UPDATE after DO at %d", p.peek().pos)
}

// parseAssignmentList parses `col = expr [, col = expr ...]` for
// UPDATE / ON CONFLICT DO UPDATE.
func (p *parser) parseAssignmentList() ([]ir.Assignment, error) {
	var out []ir.Assignment
	for {
		col, err := p.expect(tIdent, "column name")
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tEq, "="); err != nil {
			return nil, err
		}
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		out = append(out, ir.Assignment{Column: col.val, Expr: e})
		if p.accept(tComma) {
			continue
		}
		break
	}
	return out, nil
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

// parseWith consumes `WITH name AS (SELECT ...)[, ...] <stmt>` —
// non-recursive CTEs only. Each CTE plan is registered with the
// parser before the body statement parses, so its FROM clause can
// resolve CTE names. Later CTEs see earlier ones (PG-style lexical
// scoping).
func (p *parser) parseWith() (ir.Node, error) {
	p.consume() // WITH
	if p.ctes == nil {
		p.ctes = map[string]ir.Node{}
	}
	for {
		name, err := p.expect(tIdent, "CTE name")
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(kwAs, "AS"); err != nil {
			return nil, err
		}
		if _, err := p.expect(tLParen, "("); err != nil {
			return nil, err
		}
		plan, err := p.parseSelectMaybeUnion()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tRParen, ")"); err != nil {
			return nil, err
		}
		key := strings.ToLower(name.val)
		if _, dup := p.ctes[key]; dup {
			return nil, fmt.Errorf("parse: duplicate CTE name %q", name.val)
		}
		p.ctes[key] = plan
		if !p.accept(tComma) {
			break
		}
	}
	switch p.peek().kind {
	case kwSelect:
		return p.parseSelectMaybeUnion()
	case kwInsert:
		return p.parseInsert()
	case kwUpdate:
		return p.parseUpdate()
	case kwDelete:
		return p.parseDelete()
	default:
		return nil, fmt.Errorf("parse: expected SELECT/INSERT/UPDATE/DELETE after WITH at %d", p.peek().pos)
	}
}

// parseSelectMaybeUnion parses a SELECT, then any chain of
// `UNION [ALL] SELECT ...`. Members are left-associated: the first
// becomes the deepest Left in the resulting Union tree. ORDER BY /
// LIMIT remain consumed by the inner parseSelect — applying them to
// the whole union (real PG behaviour) needs grammar reshuffling that
// we'll ship if a sqlc pattern needs it.
func (p *parser) parseSelectMaybeUnion() (ir.Node, error) {
	left, err := p.parseSelect()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == kwUnion {
		p.consume() // UNION
		all := p.accept(kwAll)
		if p.peek().kind != kwSelect {
			return nil, fmt.Errorf("parse: expected SELECT after UNION at %d", p.peek().pos)
		}
		right, err := p.parseSelect()
		if err != nil {
			return nil, err
		}
		left = &ir.Union{Left: left, Right: right, All: all}
	}
	return left, nil
}

func (p *parser) parseSelect() (ir.Node, error) {
	p.consume() // SELECT
	distinct := p.accept(kwDistinct)
	exprs, names, err := p.parseSelectList()
	if err != nil {
		return nil, err
	}

	var input ir.Node = &ir.Values{Rows: [][]ir.Expr{{}}}
	if p.accept(kwFrom) {
		from, err := p.parseFromClause()
		if err != nil {
			return nil, err
		}
		input = from
	}
	if p.accept(kwWhere) {
		cond, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		input = &ir.Filter{Input: input, Cond: cond}
	}

	// GROUP BY (and the HAVING that follows) forces an aggregate plan
	// shape. Without GROUP BY the existing scalar-aggregate / Project
	// path applies — buildSelectTopOf decides which.
	var groupBy []ir.Expr
	var having ir.Expr
	if p.accept(kwGroup) {
		if _, err := p.expect(kwBy, "BY"); err != nil {
			return nil, err
		}
		groupBy, err = p.parseGroupByList()
		if err != nil {
			return nil, err
		}
	}
	if p.accept(kwHaving) {
		having, err = p.parseExpr()
		if err != nil {
			return nil, err
		}
		if groupBy == nil {
			return nil, fmt.Errorf("parse: HAVING requires GROUP BY (not supported yet)")
		}
	}

	// ORDER BY placement depends on whether we're aggregating: for
	// non-aggregate plans it sits below the Project so sort keys see
	// FROM-clause columns; for aggregate plans it sits *above* the
	// Aggregate/Project so it sees the post-aggregate output.
	hasGroupBy := groupBy != nil
	hasAggregate := false
	for _, e := range exprs {
		if isAggregateCall(e) {
			hasAggregate = true
			break
		}
	}
	aggregating := hasGroupBy || hasAggregate

	if !aggregating && p.peek().kind == kwOrder {
		p.consume() // ORDER
		if _, err := p.expect(kwBy, "BY"); err != nil {
			return nil, err
		}
		keys, err := p.parseSortKeys()
		if err != nil {
			return nil, err
		}
		input = &ir.Sort{Input: input, Keys: keys}
	}

	plan, err := buildSelectTopOf(input, exprs, names, groupBy, having)
	if err != nil {
		return nil, err
	}
	if distinct {
		plan = &ir.Distinct{Input: plan}
	}
	if aggregating && p.peek().kind == kwOrder {
		p.consume() // ORDER
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
	if err := p.skipLockingClause(); err != nil {
		return nil, err
	}
	return plan, nil
}

// skipLockingClause swallows `FOR UPDATE`, `FOR SHARE`, `FOR NO KEY
// UPDATE`, `FOR KEY SHARE` plus optional `OF ident,...` and trailing
// `NOWAIT` / `SKIP LOCKED`. We don't model row locks, so the syntax
// is accepted as a no-op — sqlc-generated transactional reads stop
// rejecting at parse time.
func (p *parser) skipLockingClause() error {
	if !p.accept(kwFor) {
		return nil
	}
	switch {
	case p.accept(kwUpdate):
	case p.acceptIdent("share"):
	case p.accept(kwKey):
		if !p.acceptIdent("share") {
			return fmt.Errorf("parse: expected SHARE after FOR KEY at %d", p.peek().pos)
		}
	case p.acceptIdent("no"):
		if !p.accept(kwKey) {
			return fmt.Errorf("parse: expected KEY after FOR NO at %d", p.peek().pos)
		}
		if !p.accept(kwUpdate) {
			return fmt.Errorf("parse: expected UPDATE after FOR NO KEY at %d", p.peek().pos)
		}
	default:
		return fmt.Errorf("parse: unexpected token %q after FOR", p.peek().val)
	}
	if p.acceptIdent("of") {
		for {
			if _, err := p.expect(tIdent, "table name"); err != nil {
				return err
			}
			if !p.accept(tComma) {
				break
			}
		}
	}
	if p.acceptIdent("nowait") {
		return nil
	}
	if p.acceptIdent("skip") {
		if !p.acceptIdent("locked") {
			return fmt.Errorf("parse: expected LOCKED after SKIP at %d", p.peek().pos)
		}
	}
	return nil
}

// parseGroupByList consumes `expr [, expr ...]`. Each entry must be a
// bare column reference for now — non-trivial GROUP BY expressions
// (e.g. `GROUP BY a + b`) need expression-equality matching in the
// Project step that wraps the Aggregate, which is a follow-up.
func (p *parser) parseGroupByList() ([]ir.Expr, error) {
	var out []ir.Expr
	for {
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, ok := e.(*ir.ColumnRef); !ok {
			return nil, fmt.Errorf("parse: GROUP BY supports only bare column references for now")
		}
		out = append(out, e)
		if p.accept(tComma) {
			continue
		}
		break
	}
	return out, nil
}

// buildSelectTopOf wraps the (already-built) FROM/WHERE/ORDER stack
// in the right shape:
//
//   - No aggregates, no GROUP BY → Project (the original case).
//   - All-aggregate, no GROUP BY → bare Aggregate (scalar-aggregate
//     case from PR #28: each call's Output names the user's column).
//   - GROUP BY (with or without aggregates) → Aggregate emits a row
//     per group with [groupKey…, aggCall…] columns, and a Project on
//     top rewires each user select item to a ColumnRef into that
//     output (synthetic "__agg_N" names for aggregates; original
//     column names for grouped refs).
func buildSelectTopOf(input ir.Node, exprs []ir.Expr, names []string, groupBy []ir.Expr, having ir.Expr) (ir.Node, error) {
	if groupBy == nil {
		// HAVING was already rejected without GROUP BY in parseSelect.
		return buildScalarOrPlainSelect(input, exprs, names)
	}
	return buildGroupedSelect(input, exprs, names, groupBy, having)
}

func buildScalarOrPlainSelect(input ir.Node, exprs []ir.Expr, names []string) (ir.Node, error) {
	hasAgg, allAgg := classifyAggregates(exprs)
	if hasAgg && !allAgg {
		return nil, fmt.Errorf("parse: mixing aggregates with non-aggregate columns requires GROUP BY")
	}
	if !hasAgg {
		return &ir.Project{Input: input, Exprs: exprs, OutputNames: names}, nil
	}
	calls := make([]ir.AggregateCall, len(exprs))
	for i, e := range exprs {
		fc := e.(*ir.FuncCall)
		var args []ir.Expr
		if !fc.Star {
			args = fc.Args
		}
		calls[i] = ir.AggregateCall{Func: fc.Name, Args: args, Output: names[i], Distinct: fc.Distinct}
	}
	return &ir.Aggregate{Input: input, Calls: calls}, nil
}

func buildGroupedSelect(input ir.Node, exprs []ir.Expr, names []string, groupBy []ir.Expr, having ir.Expr) (ir.Node, error) {
	groupBySet := make(map[string]int, len(groupBy))
	for i, e := range groupBy {
		c := e.(*ir.ColumnRef)
		groupBySet[columnRefKey(c)] = i
	}
	rw := &aggRewriter{groupBySet: groupBySet}
	projectExprs, err := rw.rewriteSelectList(exprs)
	if err != nil {
		return nil, err
	}
	var rewrittenHaving ir.Expr
	if having != nil {
		rewrittenHaving = rw.rewriteAnyExpr(having)
	}
	agg := &ir.Aggregate{Input: input, Calls: rw.calls, GroupBy: groupBy}
	var top ir.Node = agg
	if rewrittenHaving != nil {
		top = &ir.Filter{Input: top, Cond: rewrittenHaving}
	}
	return &ir.Project{Input: top, Exprs: projectExprs, OutputNames: names}, nil
}

// aggRewriter rewrites SELECT-list and HAVING expressions so they
// reference the Aggregate operator's output schema:
//   - Bare ColumnRefs that match a GROUP BY column pass through.
//   - Aggregate calls become ColumnRef{Name: "__agg_N"} and the
//     corresponding AggregateCall lands in calls.
//
// Any aggregates encountered while rewriting HAVING but missing from
// the SELECT list still get added to calls so the Aggregate node
// computes them — they just don't appear in the final Project.
type aggRewriter struct {
	groupBySet map[string]int
	calls      []ir.AggregateCall
}

func (r *aggRewriter) rewriteSelectList(exprs []ir.Expr) ([]ir.Expr, error) {
	out := make([]ir.Expr, len(exprs))
	for i, e := range exprs {
		switch v := e.(type) {
		case *ir.ColumnRef:
			if _, ok := r.groupBySet[columnRefKey(v)]; !ok {
				return nil, fmt.Errorf("parse: column %q must appear in GROUP BY or be used in an aggregate", v.Name)
			}
			out[i] = &ir.ColumnRef{Name: v.Name, Qualifier: v.Qualifier}
		default:
			if !isAggregateCall(e) {
				return nil, fmt.Errorf("parse: select expression must reference a GROUP BY column or be an aggregate")
			}
			out[i] = r.replaceAggregate(e.(*ir.FuncCall))
		}
	}
	return out, nil
}

// rewriteAnyExpr walks an arbitrary expression tree replacing every
// aggregate call with a synthetic ColumnRef. Used for HAVING which may
// embed aggregates inside boolean / comparison ops.
func (r *aggRewriter) rewriteAnyExpr(e ir.Expr) ir.Expr {
	switch v := e.(type) {
	case *ir.FuncCall:
		if isAggregateCall(v) {
			return r.replaceAggregate(v)
		}
		args := make([]ir.Expr, len(v.Args))
		for i, a := range v.Args {
			args[i] = r.rewriteAnyExpr(a)
		}
		cp := *v
		cp.Args = args
		return &cp
	case *ir.BinOp:
		return &ir.BinOp{Op: v.Op, Left: r.rewriteAnyExpr(v.Left), Right: r.rewriteAnyExpr(v.Right), T: v.T}
	case *ir.UnaryOp:
		return &ir.UnaryOp{Op: v.Op, Expr: r.rewriteAnyExpr(v.Expr), T: v.T}
	case *ir.Cast:
		return &ir.Cast{Expr: r.rewriteAnyExpr(v.Expr), T: v.T}
	case *ir.InListExpr:
		list := make([]ir.Expr, len(v.List))
		for i, le := range v.List {
			list[i] = r.rewriteAnyExpr(le)
		}
		return &ir.InListExpr{Probe: r.rewriteAnyExpr(v.Probe), List: list}
	default:
		return e
	}
}

func (r *aggRewriter) replaceAggregate(fc *ir.FuncCall) ir.Expr {
	synth := fmt.Sprintf("__agg_%d", len(r.calls))
	var args []ir.Expr
	if !fc.Star {
		args = fc.Args
	}
	r.calls = append(r.calls, ir.AggregateCall{Func: fc.Name, Args: args, Output: synth, Distinct: fc.Distinct})
	return &ir.ColumnRef{Name: synth}
}

// columnRefKey is the canonical lookup key for a ColumnRef in the
// GROUP BY set: qualifier+"."+name when qualified, plain name
// otherwise. Avoids a struct-as-map-key dance.
func columnRefKey(c *ir.ColumnRef) string {
	if c.Qualifier != "" {
		return c.Qualifier + "." + c.Name
	}
	return c.Name
}

// classifyAggregates inspects the top-level expressions of a SELECT
// list and reports whether *any* are aggregate calls and whether
// *every* one is. We only handle the all-or-nothing case until GROUP
// BY lands.
func classifyAggregates(exprs []ir.Expr) (hasAgg, allAgg bool) {
	allAgg = true
	for _, e := range exprs {
		if isAggregateCall(e) {
			hasAgg = true
		} else {
			allAgg = false
		}
	}
	return
}

func isAggregateCall(e ir.Expr) bool {
	fc, ok := e.(*ir.FuncCall)
	if !ok {
		return false
	}
	switch fc.Name {
	case "count", "sum", "min", "max", "avg", "string_agg",
		"bool_and", "bool_or", "every":
		return true
	}
	return false
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
	// `SELECT *` and `SELECT a, b, *` are both valid; expansion happens
	// in the planner against the FROM-clause schema.
	if p.peek().kind == tStar {
		p.consume()
		return &ir.StarRef{}, "", nil
	}
	// `SELECT t.*` — expand to all columns from `t`. The qualified
	// form requires the followers in this order: ident, dot, star.
	if p.peek().kind == tIdent && p.peekNext().kind == tDot && p.pos+2 < len(p.toks) && p.toks[p.pos+2].kind == tStar {
		t := p.consume()
		p.consume() // .
		p.consume() // *
		return &ir.StarRef{Qualifier: t.val}, "", nil
	}
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
		nulls := ir.NullsDefault
		if p.acceptIdent("nulls") {
			switch {
			case p.acceptIdent("first"):
				nulls = ir.NullsFirst
			case p.acceptIdent("last"):
				nulls = ir.NullsLast
			default:
				return nil, fmt.Errorf("parse: expected FIRST or LAST after NULLS at %d", p.peek().pos)
			}
		}
		out = append(out, ir.SortKey{Expr: e, Desc: desc, Nulls: nulls})
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
			// `LIMIT ALL` is the SQL-standard way to say "no limit".
			// Leave count as nil; the operator treats nil as unlimited.
			if p.accept(kwAll) {
				count = nil
				break
			}
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
			// PG accepts an optional ROW or ROWS noise word after the
			// offset value: `OFFSET 5 ROWS`. Drop it on the floor.
			if p.acceptIdent("row") || p.acceptIdent("rows") {
				_ = struct{}{}
			}
		}
	}
	return &ir.Limit{Input: plan, Count: count, Offset: offset}, nil
}
