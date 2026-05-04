package parse

import (
	"fmt"
	"reflect"
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
		return p.parseCreate()
	case kwDrop:
		return p.parseDrop()
	case kwTruncate:
		return p.parseTruncate()
	default:
		if tok.kind == tIdent && strings.EqualFold(tok.val, "alter") {
			return p.parseAlterTable()
		}
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
	lateral := p.acceptIdent("lateral")
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
	return &ir.Join{Left: left, Right: right, Cond: cond, Type: joinType, Lateral: lateral}, nil
}

func (p *parser) parseTableRef() (ir.Node, error) {
	if p.peek().kind == tLParen && p.peekNext().kind == kwSelect {
		return p.parseDerivedTable()
	}
	// Table-valued `unnest(array_expr) [AS alias]`. We recognise it
	// here rather than via a generic "table function" mechanism
	// because unnest is the only set-returning function pgmem-go
	// supports today.
	if p.peek().kind == tIdent && strings.EqualFold(p.peek().val, "unnest") && p.peekNext().kind == tLParen {
		return p.parseUnnest()
	}
	if p.peek().kind == tIdent && strings.EqualFold(p.peek().val, "generate_series") && p.peekNext().kind == tLParen {
		return p.parseGenerateSeries()
	}
	t, err := p.expect(tIdent, "table name")
	if err != nil {
		return nil, err
	}
	alias := p.parseOptionalAlias()
	if plan, ok := p.ctes[strings.ToLower(t.val)]; ok {
		// CTE reference. When the caller wrote `WITH cte AS (...) ...
		// FROM cte alias`, the alias re-tags the inner plan's columns
		// so `alias.col` resolves cleanly. Without an explicit alias
		// the CTE's own name becomes the qualifier — same way real PG
		// behaves.
		qual := alias
		if qual == "" {
			qual = t.val
		}
		return &ir.SubqueryAlias{Inner: plan, Alias: qual}, nil
	}
	return &ir.Scan{Table: t.val, Alias: alias}, nil
}

// parseUnnest consumes `unnest ( array_expr ) [AS alias]` from a
// FROM clause. The output schema is a single column whose name is
// the alias (default "unnest") and whose type comes from the array
// element type at exec.Build.
func (p *parser) parseUnnest() (ir.Node, error) {
	p.consume() // unnest
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
	alias := p.parseOptionalAlias()
	if alias == "" {
		alias = "unnest"
	}
	return &ir.Unnest{Array: arr, Alias: alias}, nil
}

// parseGenerateSeries consumes `generate_series(start, stop[, step])
// [AS alias]` from a FROM clause. The args are arbitrary integer
// expressions resolved at exec time.
func (p *parser) parseGenerateSeries() (ir.Node, error) {
	p.consume() // generate_series
	if _, err := p.expect(tLParen, "("); err != nil {
		return nil, err
	}
	start, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tComma, ","); err != nil {
		return nil, err
	}
	stop, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	var step ir.Expr
	if p.accept(tComma) {
		step, err = p.parseExpr()
		if err != nil {
			return nil, err
		}
	}
	if _, err := p.expect(tRParen, ")"); err != nil {
		return nil, err
	}
	alias := p.parseOptionalAlias()
	if alias == "" {
		alias = "generate_series"
	}
	return &ir.GenerateSeries{Start: start, Stop: stop, Step: step, Alias: alias}, nil
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
	if p.accept(kwFrom) {
		from, err := p.parseFromClause()
		if err != nil {
			return nil, err
		}
		stmt.From = from
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
	if p.acceptIdent("using") {
		using, err := p.parseFromClause()
		if err != nil {
			return nil, err
		}
		stmt.Using = using
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

// parseAlterTable consumes `ALTER TABLE [IF EXISTS] name <action>`
// where <action> is one of:
//
//	ADD [COLUMN] [IF NOT EXISTS] col type [constraints]
//	DROP [COLUMN] [IF EXISTS] col [CASCADE|RESTRICT]
//	RENAME [COLUMN] old TO new
//
// Multi-action ALTERs (comma-separated) are not supported.
func (p *parser) parseAlterTable() (ir.Node, error) {
	p.consume() // ALTER (ident)
	if _, err := p.expect(kwTable, "TABLE"); err != nil {
		return nil, err
	}
	if p.accept(kwIf) {
		if _, err := p.expect(kwExists, "EXISTS"); err != nil {
			return nil, err
		}
		// We don't model "alter if exists" specially — if the table
		// is missing the exec layer surfaces a clear error.
	}
	tbl, err := p.expect(tIdent, "table name")
	if err != nil {
		return nil, err
	}
	switch {
	case p.acceptIdent("add"):
		return p.parseAlterTableAdd(tbl.val)
	case p.accept(kwDrop):
		return p.parseAlterTableDrop(tbl.val)
	case p.acceptIdent("rename"):
		return p.parseAlterTableRename(tbl.val)
	case p.acceptIdent("alter"):
		return p.parseAlterTableAlterColumn(tbl.val)
	default:
		return nil, fmt.Errorf("parse: expected ADD/DROP/RENAME/ALTER after ALTER TABLE %s at %d", tbl.val, p.peek().pos)
	}
}

func (p *parser) parseAlterTableAdd(table string) (ir.Node, error) {
	p.acceptIdent("column") // optional
	// IF NOT EXISTS: accepted for parser compatibility, no extra
	// behaviour — adding the same column twice will fail at the
	// catalog layer regardless.
	if p.accept(kwIf) {
		if !p.accept(kwNot) {
			return nil, fmt.Errorf("parse: expected NOT after IF at %d", p.peek().pos)
		}
		if _, err := p.expect(kwExists, "EXISTS"); err != nil {
			return nil, err
		}
	}
	col, err := p.parseColumnDef()
	if err != nil {
		return nil, err
	}
	return &ir.AlterTable{Table: table, Action: ir.AlterTableAddColumn, AddCol: col}, nil
}

func (p *parser) parseAlterTableDrop(table string) (ir.Node, error) {
	p.acceptIdent("column") // optional
	ifExists := false
	if p.accept(kwIf) {
		if _, err := p.expect(kwExists, "EXISTS"); err != nil {
			return nil, err
		}
		ifExists = true
	}
	name, err := p.expect(tIdent, "column name")
	if err != nil {
		return nil, err
	}
	// Optional CASCADE/RESTRICT — we don't model dependent objects,
	// so both are accepted and ignored. CASCADE is a real keyword;
	// RESTRICT lives only as a context-ident in our lexer.
	if !p.accept(kwCascade) {
		p.acceptIdent("restrict")
	}
	return &ir.AlterTable{Table: table, Action: ir.AlterTableDropColumn, DropName: name.val, IfExistsCol: ifExists}, nil
}

func (p *parser) parseAlterTableRename(table string) (ir.Node, error) {
	p.acceptIdent("column") // optional
	old, err := p.expect(tIdent, "column name")
	if err != nil {
		return nil, err
	}
	if !p.acceptIdent("to") {
		return nil, fmt.Errorf("parse: expected TO after RENAME column at %d", p.peek().pos)
	}
	newName, err := p.expect(tIdent, "new column name")
	if err != nil {
		return nil, err
	}
	return &ir.AlterTable{Table: table, Action: ir.AlterTableRenameColumn, RenameOld: old.val, RenameNew: newName.val}, nil
}

// parseAlterTableAlterColumn consumes the per-column action that
// follows `ALTER TABLE name ALTER [COLUMN] col`. Today we recognise
// SET NOT NULL and DROP NOT NULL — the other ALTER COLUMN forms
// (SET DEFAULT, DROP DEFAULT, TYPE …) are not yet supported.
func (p *parser) parseAlterTableAlterColumn(table string) (ir.Node, error) {
	p.acceptIdent("column") // optional
	col, err := p.expect(tIdent, "column name")
	if err != nil {
		return nil, err
	}
	switch {
	case p.accept(kwSet):
		if !p.accept(kwNot) {
			return nil, fmt.Errorf("parse: expected NOT after ALTER COLUMN %s SET at %d", col.val, p.peek().pos)
		}
		if _, err := p.expect(kwNull, "NULL"); err != nil {
			return nil, err
		}
		return &ir.AlterTable{Table: table, Action: ir.AlterTableSetNotNull, AlterCol: col.val}, nil
	case p.accept(kwDrop):
		if !p.accept(kwNot) {
			return nil, fmt.Errorf("parse: expected NOT after ALTER COLUMN %s DROP at %d", col.val, p.peek().pos)
		}
		if _, err := p.expect(kwNull, "NULL"); err != nil {
			return nil, err
		}
		return &ir.AlterTable{Table: table, Action: ir.AlterTableDropNotNull, AlterCol: col.val}, nil
	default:
		return nil, fmt.Errorf("parse: expected SET/DROP NOT NULL after ALTER COLUMN %s at %d", col.val, p.peek().pos)
	}
}

// --- CREATE TABLE ---

// parseCreate dispatches between CREATE TABLE / VIEW / INDEX. The
// CREATE keyword has not been consumed yet. INDEX form also accepts
// the optional UNIQUE prefix (`CREATE UNIQUE INDEX …`) — matched
// here.
func (p *parser) parseCreate() (ir.Node, error) {
	next := p.peekNext()
	if next.kind == kwTable {
		return p.parseCreateTable()
	}
	if next.kind == tIdent {
		switch strings.ToLower(next.val) {
		case "view":
			return p.parseCreateView()
		case "index", "concurrently":
			return p.parseCreateIndex()
		}
	}
	if next.kind == kwUnique {
		// `CREATE UNIQUE INDEX …` — the UNIQUE is part of the
		// index spec, not the start of an inline constraint.
		return p.parseCreateIndex()
	}
	return nil, fmt.Errorf("parse: unexpected token after CREATE: %q", next.val)
}

// parseDrop dispatches between DROP TABLE / VIEW / INDEX.
func (p *parser) parseDrop() (ir.Node, error) {
	next := p.peekNext()
	if next.kind == kwTable {
		return p.parseDropTable()
	}
	if next.kind == tIdent {
		switch strings.ToLower(next.val) {
		case "view":
			return p.parseDropView()
		case "index":
			return p.parseDropIndex()
		}
	}
	return nil, fmt.Errorf("parse: unexpected token after DROP: %q", next.val)
}

// parseCreateIndex consumes the wide `CREATE [UNIQUE] INDEX
// [CONCURRENTLY] [IF NOT EXISTS] [name] ON table [USING method] (
// expr_list ) [WHERE …]` form and discards everything but the name
// and table — pgmem-go has no real index machinery, so the operator
// is a no-op DDL.
func (p *parser) parseCreateIndex() (ir.Node, error) {
	p.consume() // CREATE
	p.accept(kwUnique)
	concurrentlySeen := p.acceptIdent("concurrently")
	if !p.acceptIdent("index") {
		return nil, fmt.Errorf("parse: expected INDEX at %d", p.peek().pos)
	}
	if !concurrentlySeen {
		concurrentlySeen = p.acceptIdent("concurrently")
	}
	ifNotExists := false
	if p.accept(kwIf) {
		if !p.accept(kwNot) {
			return nil, fmt.Errorf("parse: expected NOT after IF at %d", p.peek().pos)
		}
		if _, err := p.expect(kwExists, "EXISTS"); err != nil {
			return nil, err
		}
		ifNotExists = true
	}
	// Optional index name — present unless caller skipped to ON.
	var name string
	if p.peek().kind == tIdent {
		name = p.consume().val
	}
	if !p.accept(kwOn) {
		return nil, fmt.Errorf("parse: expected ON in CREATE INDEX at %d", p.peek().pos)
	}
	tableTok, err := p.expect(tIdent, "table name")
	if err != nil {
		return nil, err
	}
	// Optional USING <method>.
	if p.acceptIdent("using") {
		if _, err := p.expect(tIdent, "index method"); err != nil {
			return nil, err
		}
	}
	// Column / expression list — discarded.
	if _, err := p.expect(tLParen, "("); err != nil {
		return nil, err
	}
	if err := skipBalancedParens(p); err != nil {
		return nil, err
	}
	// Optional partial-index predicate.
	if p.accept(kwWhere) {
		if _, err := p.parseExpr(); err != nil {
			return nil, err
		}
	}
	return &ir.CreateIndex{
		Name:         name,
		Table:        tableTok.val,
		IfNotExists:  ifNotExists,
		Concurrently: concurrentlySeen,
	}, nil
}

// parseDropIndex consumes `DROP INDEX [CONCURRENTLY] [IF EXISTS]
// name [, name ...]` and ignores everything but the first name.
// pgmem-go doesn't track indexes, so the operator is a no-op.
func (p *parser) parseDropIndex() (ir.Node, error) {
	p.consume() // DROP
	if !p.acceptIdent("index") {
		return nil, fmt.Errorf("parse: expected INDEX at %d", p.peek().pos)
	}
	p.acceptIdent("concurrently")
	ifExists := false
	if p.accept(kwIf) {
		if _, err := p.expect(kwExists, "EXISTS"); err != nil {
			return nil, err
		}
		ifExists = true
	}
	name, err := p.expect(tIdent, "index name")
	if err != nil {
		return nil, err
	}
	// Drop additional names — we still produce a single ir.DropIndex
	// node; there's nothing to actually drop in any case.
	for p.accept(tComma) {
		if _, err := p.expect(tIdent, "index name"); err != nil {
			return nil, err
		}
	}
	return &ir.DropIndex{Name: name.val, IfExists: ifExists}, nil
}

// skipBalancedParens consumes tokens until the matching ')' that
// pairs with the most recently consumed '('. Nested parens count
// against the depth so `(a, (b, c))` round-trips. The `(` has
// already been consumed by the caller.
func skipBalancedParens(p *parser) error {
	depth := 1
	for depth > 0 {
		switch p.peek().kind {
		case tEOF:
			return fmt.Errorf("parse: unterminated parenthesised list")
		case tLParen:
			depth++
		case tRParen:
			depth--
		}
		p.consume()
	}
	return nil
}

// parseCreateView consumes `CREATE VIEW name AS SELECT …`.
func (p *parser) parseCreateView() (ir.Node, error) {
	p.consume() // CREATE
	if !p.acceptIdent("view") {
		return nil, fmt.Errorf("parse: expected VIEW at %d", p.peek().pos)
	}
	name, err := p.expect(tIdent, "view name")
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(kwAs, "AS"); err != nil {
		return nil, err
	}
	plan, err := p.parseSelectMaybeUnion()
	if err != nil {
		return nil, err
	}
	return &ir.CreateView{Name: name.val, Plan: plan}, nil
}

// parseDropView consumes `DROP VIEW [IF EXISTS] name`.
func (p *parser) parseDropView() (ir.Node, error) {
	p.consume() // DROP
	if !p.acceptIdent("view") {
		return nil, fmt.Errorf("parse: expected VIEW at %d", p.peek().pos)
	}
	ifExists := false
	if p.accept(kwIf) {
		if _, err := p.expect(kwExists, "EXISTS"); err != nil {
			return nil, err
		}
		ifExists = true
	}
	name, err := p.expect(tIdent, "view name")
	if err != nil {
		return nil, err
	}
	return &ir.DropView{Name: name.val, IfExists: ifExists}, nil
}

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
		// Table-level constraints (`PRIMARY KEY (cols)`, `UNIQUE
		// (cols)`, `FOREIGN KEY (cols) REFERENCES …`) sit alongside
		// column definitions in the parens. We accept the common
		// shapes here for parser compatibility — the actual
		// constraint enforcement piggy-backs on the per-column flags
		// already set by NOT NULL declarations and on ON CONFLICT's
		// value-match path. Composite uniqueness arrives when a real
		// query needs it.
		if p.peek().kind == kwPrimary || p.peek().kind == kwUnique {
			if err := p.skipTableConstraint(); err != nil {
				return nil, err
			}
			if p.accept(tComma) {
				continue
			}
			break
		}
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

// skipTableConstraint consumes a `PRIMARY KEY (cols)` or
// `UNIQUE (cols)` table-level constraint without recording it. The
// parser exists to accept real PG schemas; the constraint behaviour
// it would imply is already covered by per-column flags + ON
// CONFLICT's value-match path.
func (p *parser) skipTableConstraint() error {
	if p.accept(kwPrimary) {
		if !p.accept(kwKey) {
			return fmt.Errorf("parse: expected KEY after PRIMARY at %d", p.peek().pos)
		}
	} else if !p.accept(kwUnique) {
		return fmt.Errorf("parse: unexpected token %q in CREATE TABLE", p.peek().val)
	}
	if _, err := p.expect(tLParen, "("); err != nil {
		return err
	}
	for {
		if _, err := p.expect(tIdent, "constraint column name"); err != nil {
			return err
		}
		if !p.accept(tComma) {
			break
		}
	}
	if _, err := p.expect(tRParen, ")"); err != nil {
		return err
	}
	return nil
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
	colTypeName := typeName.val
	if p.accept(tLBracket) {
		if !p.accept(tRBracket) {
			return ir.ColumnDef{}, fmt.Errorf("parse: expected ']' after '[' at %d", p.peek().pos)
		}
		colTypeName += "[]"
	}
	def := ir.ColumnDef{Name: name.val}
	if t, auto, ok := resolveSerial(colTypeName); ok {
		// SERIAL / BIGSERIAL: Postgres desugars these to (int4|int8) +
		// NOT NULL + DEFAULT nextval(...). We squash that into Auto +
		// NotNull on the catalog column.
		def.Type = t
		def.Auto = auto
		def.NotNull = true
	} else {
		t, ok := types.ByName(colTypeName)
		if !ok {
			return ir.ColumnDef{}, fmt.Errorf("parse: unknown type %q", colTypeName)
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
	case p.acceptIdent("default"):
		expr, err := p.parseExpr()
		if err != nil {
			return false, err
		}
		def.Default = expr
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
	} else if p.acceptIdent("default") {
		// `INSERT INTO t DEFAULT VALUES` inserts a single row with
		// every column either auto-filled or NULL. We don't model
		// per-column DEFAULT exprs; the row goes in via the existing
		// auto-column path with NULL for everything else.
		if !p.accept(kwValues) {
			return nil, fmt.Errorf("parse: expected VALUES after DEFAULT at %d", p.peek().pos)
		}
		stmt.DefaultValues = true
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
// `UNION [ALL] SELECT ...`. Members are left-associated. When a
// UNION is present, ORDER BY / LIMIT / FOR-locking parse once at the
// end and apply to the whole union — matching real PG. For a plain
// solo SELECT the trailing clauses attach with full alias-aware
// resolution against the SELECT list.
func (p *parser) parseSelectMaybeUnion() (ir.Node, error) {
	left, lExprs, lNames, err := p.parseSelectCoreInfo()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != kwUnion {
		return p.parseSelectTailSolo(left, lExprs, lNames)
	}
	for p.peek().kind == kwUnion {
		p.consume() // UNION
		all := p.accept(kwAll)
		if p.peek().kind != kwSelect {
			return nil, fmt.Errorf("parse: expected SELECT after UNION at %d", p.peek().pos)
		}
		right, err := p.parseSelectCore()
		if err != nil {
			return nil, err
		}
		left = &ir.Union{Left: left, Right: right, All: all}
	}
	return p.parseSelectTailUnion(left)
}

// parseSelectCoreInfo is parseSelectCore but also reports the
// SELECT-list expressions and aliases. The non-union path uses these
// to rewrite trailing ORDER BY references that name an output alias.
func (p *parser) parseSelectCoreInfo() (ir.Node, []ir.Expr, []string, error) {
	plan, exprs, names, err := p.parseSelectCoreReturning()
	if err != nil {
		return nil, nil, nil, err
	}
	return plan, exprs, names, nil
}

// parseSelectTailSolo consumes ORDER BY / LIMIT / FOR-locking after a
// solo SELECT plan and rewrites bare ORDER BY column refs that
// match an output alias to the underlying SELECT-list expression.
// The Sort lands UNDER the Project so sort keys see base columns
// (real PG behaviour).
func (p *parser) parseSelectTailSolo(plan ir.Node, exprs []ir.Expr, names []string) (ir.Node, error) {
	if p.peek().kind == kwOrder {
		p.consume()
		if _, err := p.expect(kwBy, "BY"); err != nil {
			return nil, err
		}
		keys, err := p.parseSortKeys()
		if err != nil {
			return nil, err
		}
		// For aggregating plans the Project sits over an Aggregate;
		// sort keys must resolve against Project's output schema, so
		// the Sort wraps the plan and bare aliases work directly. For
		// non-aggregating plans the Sort goes under the Project so
		// sort keys can also reference base columns the SELECT list
		// dropped — we rewrite alias references to the underlying
		// SELECT-list expression in that case.
		topProject, _ := topProjectInput(plan)
		if topProject != nil && !hasAggregateBelow(topProject) {
			rewriteSortKeysByAlias(keys, exprs, names)
		}
		plan = insertSortBelowProject(plan, keys)
	}
	if hasLimitOrOffset(p) {
		var err error
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

// parseSelectTailUnion consumes the same trailing clauses but
// applies the Sort on top of the unioned plan. The merged Union
// schema doesn't carry SELECT-list aliases, so sort keys must
// resolve against output column names directly — same as real PG.
func (p *parser) parseSelectTailUnion(plan ir.Node) (ir.Node, error) {
	if p.peek().kind == kwOrder {
		p.consume()
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
		var err error
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

// insertSortBelowProject splices a Sort beneath the top Project (or
// Distinct(Project(...))) of plan when the Project doesn't sit over
// an Aggregate. For aggregating selects the Sort wraps the plan
// instead, so sort keys see the post-aggregate output schema (which
// is the only place GROUP BY columns and aggregate results exist).
func insertSortBelowProject(plan ir.Node, keys []ir.SortKey) ir.Node {
	switch p := plan.(type) {
	case *ir.Project:
		if !hasAggregateBelow(p.Input) {
			p.Input = &ir.Sort{Input: p.Input, Keys: keys}
			return p
		}
	case *ir.Distinct:
		if pj, ok := p.Input.(*ir.Project); ok && !hasAggregateBelow(pj.Input) {
			pj.Input = &ir.Sort{Input: pj.Input, Keys: keys}
			return p
		}
	}
	return &ir.Sort{Input: plan, Keys: keys}
}

// topProjectInput returns the input of the topmost Project in plan
// (peeling Distinct), or (nil, false) if there is no Project up top.
func topProjectInput(plan ir.Node) (ir.Node, bool) {
	switch x := plan.(type) {
	case *ir.Project:
		return x.Input, true
	case *ir.Distinct:
		if pj, ok := x.Input.(*ir.Project); ok {
			return pj.Input, true
		}
	}
	return nil, false
}

// hasAggregateBelow reports whether n contains an ir.Aggregate
// reachable through pure relational wrappers.
func hasAggregateBelow(n ir.Node) bool {
	for {
		switch x := n.(type) {
		case *ir.Aggregate:
			return true
		case *ir.Filter:
			n = x.Input
		case *ir.Project:
			n = x.Input
		case *ir.Sort:
			n = x.Input
		case *ir.Distinct:
			n = x.Input
		default:
			return false
		}
	}
}

// parseSelect handles a complete top-level SELECT including any
// trailing ORDER BY / LIMIT / FOR-locking. It's the entry point for
// solo subqueries (EXISTS, IN, derived tables, scalar subqueries).
// Top-level statements go through parseSelectMaybeUnion so a UNION
// chain's trailing clauses attach to the union as a whole.
func (p *parser) parseSelect() (ir.Node, error) {
	plan, exprs, names, err := p.parseSelectCoreReturning()
	if err != nil {
		return nil, err
	}
	return p.parseSelectTailSolo(plan, exprs, names)
}

// parseSelectCore parses a single SELECT block — clauses up through
// Project / Aggregate construction — and returns the plan without
// consuming trailing ORDER BY / LIMIT / locking. parseSelect and
// parseSelectMaybeUnion both build on this: the former adds the tail
// directly, the latter loops on UNION first and lets the tail apply
// once at the end.
func (p *parser) parseSelectCore() (ir.Node, error) {
	plan, _, _, err := p.parseSelectCoreReturning()
	return plan, err
}

// parseSelectCoreReturning is the workhorse that parseSelectCore and
// parseSelectCoreInfo wrap. It returns the plan plus the SELECT-list
// expressions and aliases the caller needs for alias-aware tail
// processing.
func (p *parser) parseSelectCoreReturning() (ir.Node, []ir.Expr, []string, error) {
	p.consume() // SELECT
	distinct := p.accept(kwDistinct)
	var distinctOn []ir.Expr
	if distinct && p.accept(kwOn) {
		if _, err := p.expect(tLParen, "("); err != nil {
			return nil, nil, nil, err
		}
		for {
			e, err := p.parseExpr()
			if err != nil {
				return nil, nil, nil, err
			}
			distinctOn = append(distinctOn, e)
			if !p.accept(tComma) {
				break
			}
		}
		if _, err := p.expect(tRParen, ")"); err != nil {
			return nil, nil, nil, err
		}
	}
	exprs, names, err := p.parseSelectList()
	if err != nil {
		return nil, nil, nil, err
	}

	var input ir.Node = &ir.Values{Rows: [][]ir.Expr{{}}}
	if p.accept(kwFrom) {
		from, err := p.parseFromClause()
		if err != nil {
			return nil, nil, nil, err
		}
		input = from
	}
	if p.accept(kwWhere) {
		cond, err := p.parseExpr()
		if err != nil {
			return nil, nil, nil, err
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
			return nil, nil, nil, err
		}
		groupBy, err = p.parseGroupByList()
		if err != nil {
			return nil, nil, nil, err
		}
	}
	if p.accept(kwHaving) {
		having, err = p.parseExpr()
		if err != nil {
			return nil, nil, nil, err
		}
		if groupBy == nil {
			return nil, nil, nil, fmt.Errorf("parse: HAVING requires GROUP BY (not supported yet)")
		}
	}

	// Window functions are processed before the Project: their inputs
	// are FROM-clause columns, but the Project can reference their
	// synthetic outputs as columns. extractWindowCalls walks the
	// SELECT list and rewrites windowed FuncCalls into ColumnRefs
	// pointing at the synthetic outputs of a wrapping Window node.
	winCalls, rewritten := extractWindowCalls(exprs)
	if len(winCalls) > 0 {
		input = &ir.Window{Input: input, Calls: winCalls}
		exprs = rewritten
	}

	plan, err := buildSelectTopOf(input, exprs, names, groupBy, having)
	if err != nil {
		return nil, nil, nil, err
	}
	if distinct {
		plan = &ir.Distinct{Input: plan, On: distinctOn}
	}
	return plan, exprs, names, nil
}

// extractWindowCalls walks the SELECT-list expressions, lifts every
// window FuncCall into a WindowCall slice, and substitutes a
// ColumnRef pointing at the synthetic output. A windowed call buried
// inside an arithmetic / cast expression works the same way.
func extractWindowCalls(exprs []ir.Expr) ([]ir.WindowCall, []ir.Expr) {
	var calls []ir.WindowCall
	rewritten := make([]ir.Expr, len(exprs))
	for i, e := range exprs {
		rewritten[i] = rewriteWindowExpr(e, &calls)
	}
	return calls, rewritten
}

// rewriteWindowExpr returns a copy of e with every windowed FuncCall
// replaced by a ColumnRef. Newly-extracted calls land in *calls.
func rewriteWindowExpr(e ir.Expr, calls *[]ir.WindowCall) ir.Expr {
	switch v := e.(type) {
	case *ir.FuncCall:
		if v.Window != nil {
			synth := fmt.Sprintf("__win_%d", len(*calls))
			*calls = append(*calls, ir.WindowCall{
				Func:   v.Name,
				Args:   v.Args,
				Spec:   *v.Window,
				Output: synth,
			})
			return &ir.ColumnRef{Name: synth}
		}
		args := make([]ir.Expr, len(v.Args))
		for i, a := range v.Args {
			args[i] = rewriteWindowExpr(a, calls)
		}
		cp := *v
		cp.Args = args
		return &cp
	case *ir.BinOp:
		return &ir.BinOp{
			Op: v.Op, T: v.T,
			Left:  rewriteWindowExpr(v.Left, calls),
			Right: rewriteWindowExpr(v.Right, calls),
		}
	case *ir.UnaryOp:
		return &ir.UnaryOp{Op: v.Op, T: v.T, Expr: rewriteWindowExpr(v.Expr, calls)}
	case *ir.Cast:
		return &ir.Cast{T: v.T, Expr: rewriteWindowExpr(v.Expr, calls)}
	default:
		return e
	}
}

// rewriteSortKeysByAlias replaces a bare-name sort key with the
// SELECT-list expression it aliases. Lets `ORDER BY status` work for
// queries like `SELECT … CASE WHEN … END AS status FROM …` — the
// rewrite must happen before the Sort goes under the Project so the
// underlying expression resolves against the FROM-clause schema.
func rewriteSortKeysByAlias(keys []ir.SortKey, exprs []ir.Expr, names []string) {
	if len(names) == 0 {
		return
	}
	aliasMap := make(map[string]ir.Expr, len(names))
	for i, n := range names {
		if n == "" {
			continue
		}
		aliasMap[n] = exprs[i]
	}
	for i, k := range keys {
		ref, ok := k.Expr.(*ir.ColumnRef)
		if !ok || ref.Qualifier != "" {
			continue
		}
		if expr, ok := aliasMap[ref.Name]; ok {
			keys[i].Expr = expr
		}
	}
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

// parseGroupByList consumes `expr [, expr ...]`. Bare column refs and
// arbitrary expressions are both accepted — the SELECT-list rewriter
// matches expressions structurally so `GROUP BY date_trunc('hour',
// created_at)` works alongside the simple `GROUP BY col` form.
func (p *parser) parseGroupByList() ([]ir.Expr, error) {
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
		calls[i] = ir.AggregateCall{Func: fc.Name, Args: args, Output: names[i], Distinct: fc.Distinct, Filter: fc.Filter}
	}
	return &ir.Aggregate{Input: input, Calls: calls}, nil
}

func buildGroupedSelect(input ir.Node, exprs []ir.Expr, names []string, groupBy []ir.Expr, having ir.Expr) (ir.Node, error) {
	groupBySet := make(map[string]int, len(groupBy))
	rw := &aggRewriter{groupBySet: groupBySet, groupByExprs: groupBy}
	for i, e := range groupBy {
		if c, ok := e.(*ir.ColumnRef); ok {
			groupBySet[columnRefKey(c)] = i
		}
	}
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
	groupBySet   map[string]int
	groupByExprs []ir.Expr
	calls        []ir.AggregateCall
}

// matchGroupByExpr returns the synthetic output name (and true) when
// e is structurally equal to one of the GROUP BY expressions. Used so
// `SELECT date_trunc('hour', ts) … GROUP BY date_trunc('hour', ts)`
// produces a ColumnRef to the aggregate's group-output column rather
// than recursing into ts and complaining that ts isn't in GROUP BY.
func (r *aggRewriter) matchGroupByExpr(e ir.Expr) (string, bool) {
	if c, ok := e.(*ir.ColumnRef); ok {
		if _, in := r.groupBySet[columnRefKey(c)]; in {
			return c.Name, true
		}
	}
	for i, g := range r.groupByExprs {
		if exprStructEqual(e, g) {
			if c, ok := g.(*ir.ColumnRef); ok {
				return c.Name, true
			}
			return groupExprName(i), true
		}
	}
	return "", false
}

// groupExprName is the synthetic output name an arbitrary GROUP BY
// expression takes when it isn't a bare column ref. exec/aggregate
// uses the same shape via ir.Aggregate.GroupBy ordering.
func groupExprName(i int) string { return fmt.Sprintf("__group_%d", i) }

// exprStructEqual is a shallow structural equality on the IR
// expression tree — enough to spot "select-list expression is the
// same as a GROUP BY entry." reflect.DeepEqual works because parser-
// emitted expressions don't carry resolved-only fields yet (those
// land in resolveExpr later).
func exprStructEqual(a, b ir.Expr) bool {
	return reflect.DeepEqual(a, b)
}

func (r *aggRewriter) rewriteSelectList(exprs []ir.Expr) ([]ir.Expr, error) {
	out := make([]ir.Expr, len(exprs))
	for i, e := range exprs {
		if c, ok := e.(*ir.ColumnRef); ok {
			if _, in := r.groupBySet[columnRefKey(c)]; !in {
				return nil, fmt.Errorf("parse: column %q must appear in GROUP BY or be used in an aggregate", c.Name)
			}
			out[i] = &ir.ColumnRef{Name: c.Name, Qualifier: c.Qualifier}
			continue
		}
		// Walk arbitrary expressions so aggregates buried inside
		// casts, arithmetic, or other function calls are detected.
		// Bare column refs that aren't in GROUP BY surface as an
		// error during the walk.
		rewritten, err := r.rewriteSelectExpr(e)
		if err != nil {
			return nil, err
		}
		out[i] = rewritten
	}
	return out, nil
}

// rewriteSelectExpr walks an expression and replaces aggregate calls
// with synthetic ColumnRefs while validating that any non-aggregated
// column reference appears in GROUP BY.
func (r *aggRewriter) rewriteSelectExpr(e ir.Expr) (ir.Expr, error) {
	// If the whole expression matches a GROUP BY entry structurally,
	// the aggregate already emits its value as one of the group output
	// columns — replace with a ColumnRef pointing at that slot.
	if name, ok := r.matchGroupByExpr(e); ok {
		return &ir.ColumnRef{Name: name}, nil
	}
	switch v := e.(type) {
	case *ir.ColumnRef:
		if _, ok := r.groupBySet[columnRefKey(v)]; !ok {
			return nil, fmt.Errorf("parse: column %q must appear in GROUP BY or be used in an aggregate", v.Name)
		}
		return &ir.ColumnRef{Name: v.Name, Qualifier: v.Qualifier}, nil
	case *ir.FuncCall:
		if isAggregateCall(v) {
			return r.replaceAggregate(v), nil
		}
		args := make([]ir.Expr, len(v.Args))
		for i, a := range v.Args {
			rr, err := r.rewriteSelectExpr(a)
			if err != nil {
				return nil, err
			}
			args[i] = rr
		}
		cp := *v
		cp.Args = args
		return &cp, nil
	case *ir.BinOp:
		l, err := r.rewriteSelectExpr(v.Left)
		if err != nil {
			return nil, err
		}
		rgt, err := r.rewriteSelectExpr(v.Right)
		if err != nil {
			return nil, err
		}
		return &ir.BinOp{Op: v.Op, Left: l, Right: rgt, T: v.T}, nil
	case *ir.UnaryOp:
		inner, err := r.rewriteSelectExpr(v.Expr)
		if err != nil {
			return nil, err
		}
		return &ir.UnaryOp{Op: v.Op, Expr: inner, T: v.T}, nil
	case *ir.Cast:
		inner, err := r.rewriteSelectExpr(v.Expr)
		if err != nil {
			return nil, err
		}
		return &ir.Cast{Expr: inner, T: v.T}, nil
	case *ir.Case:
		out := *v
		if v.Operand != nil {
			op, err := r.rewriteSelectExpr(v.Operand)
			if err != nil {
				return nil, err
			}
			out.Operand = op
		}
		out.Whens = make([]ir.CaseWhen, len(v.Whens))
		for i, w := range v.Whens {
			m, err := r.rewriteSelectExpr(w.Match)
			if err != nil {
				return nil, err
			}
			rs, err := r.rewriteSelectExpr(w.Result)
			if err != nil {
				return nil, err
			}
			out.Whens[i] = ir.CaseWhen{Match: m, Result: rs}
		}
		if v.Else != nil {
			el, err := r.rewriteSelectExpr(v.Else)
			if err != nil {
				return nil, err
			}
			out.Else = el
		}
		return &out, nil
	case *ir.Literal, *ir.ParamRef:
		return v, nil
	default:
		return e, nil
	}
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
	r.calls = append(r.calls, ir.AggregateCall{Func: fc.Name, Args: args, Output: synth, Distinct: fc.Distinct, Filter: fc.Filter})
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
		"bool_and", "bool_or", "every", "array_agg":
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
