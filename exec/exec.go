// Package exec walks an ir.Node tree against a storage.Txn and
// produces rows. Operators map almost 1:1 to IR node kinds.
package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/pgmem-go/catalog"
	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/storage"
	"github.com/kaeawc/pgmem-go/types"
)

// Row is a single tuple flowing through an operator pipeline.
type Row []any

// Column is one slot in an operator's output schema. The wire layer
// reads OutputSchema before pulling rows so it can emit RowDescription.
//
// Qualifier is the source table for joined or scanned columns and is
// what allows `users.id` to resolve unambiguously when both sides of a
// join expose an `id`. It is empty for projected/synthetic columns
// since `SELECT id ...` doesn't give the result an inherited table.
type Column struct {
	Qualifier string
	Name      string
	Type      types.Type
}

// Param is one bound query parameter. The type comes from the Parse
// OID (or, lacking that, an inference from the Bind format we hope
// nobody hits in M2).
type Param struct {
	Type  types.Type
	Value any
}

// Env bundles everything an operator pipeline needs to be built and
// run. We pass it explicitly rather than storing it on package-level
// state so a single process can host many isolated servers.
//
// Now is the clock the now() builtin reads when set. nil means "use
// the real wall clock"; tests set this through Server.SetNow to make
// time deterministic.
type Env struct {
	Schema catalog.Schema
	Engine storage.Engine
	Txn    storage.Txn
	Params []Param
	Now    func() time.Time
}

// Operator is the runtime instantiation of an ir.Node.
type Operator interface {
	OutputSchema() []Column
	Next(ctx context.Context) (Row, error) // returns io.EOF at end
	Close() error
}

// Build compiles an IR plan into an operator pipeline against the
// given environment.
func Build(plan ir.Node, env *Env) (Operator, error) {
	switch p := plan.(type) {
	case *ir.Scan:
		return buildScan(p, env)
	case *ir.Project:
		return buildProject(p, env)
	case *ir.Values:
		return buildValues(p, env)
	case *ir.Filter:
		return buildFilter(p, env)
	case *ir.Join:
		return buildJoin(p, env)
	case *ir.Sort:
		return buildSort(p, env)
	case *ir.Limit:
		return buildLimit(p, env)
	case *ir.CreateTable:
		return buildCreateTable(p, env), nil
	case *ir.DropTable:
		return buildDropTable(p, env), nil
	case *ir.Insert:
		return buildInsert(p, env)
	case *ir.Delete:
		return buildDelete(p, env)
	case *ir.Update:
		return buildUpdate(p, env)
	default:
		return nil, fmt.Errorf("exec: unsupported plan node %T", plan)
	}
}

// --- Scan ---

func buildScan(p *ir.Scan, env *Env) (Operator, error) {
	ct, ok := env.Schema.Table(p.Table)
	if !ok {
		return nil, fmt.Errorf("exec: unknown table %q", p.Table)
	}
	st, ok := env.Txn.Table(p.Table)
	if !ok {
		return nil, fmt.Errorf("exec: storage missing table %q", p.Table)
	}
	cols := make([]Column, len(ct.Columns))
	for i, c := range ct.Columns {
		cols[i] = Column{Qualifier: p.Table, Name: c.Name, Type: c.Type}
	}
	return &scanOp{cols: cols, rows: storageRowsToExec(st.Rows())}, nil
}

func storageRowsToExec(in []storage.Row) []Row {
	out := make([]Row, len(in))
	for i, r := range in {
		out[i] = Row(r)
	}
	return out
}

type scanOp struct {
	cols []Column
	rows []Row
	pos  int
}

func (s *scanOp) OutputSchema() []Column { return s.cols }
func (s *scanOp) Close() error           { return nil }

func (s *scanOp) Next(_ context.Context) (Row, error) {
	if s.pos >= len(s.rows) {
		return nil, io.EOF
	}
	r := s.rows[s.pos]
	s.pos++
	return r, nil
}

// --- Project ---

func buildProject(p *ir.Project, env *Env) (Operator, error) {
	in, err := Build(p.Input, env)
	if err != nil {
		return nil, err
	}
	inSchema := in.OutputSchema()
	exprs := make([]ir.Expr, len(p.Exprs))
	cols := make([]Column, len(p.Exprs))
	for i, e := range p.Exprs {
		resolved, err := resolveExpr(e, inSchema, env)
		if err != nil {
			return nil, err
		}
		exprs[i] = resolved
		name := ""
		if i < len(p.OutputNames) {
			name = p.OutputNames[i]
		}
		cols[i] = Column{Name: name, Type: resolved.Type()}
	}
	return &projectOp{in: in, cols: cols, exprs: exprs, env: env}, nil
}

type projectOp struct {
	in    Operator
	cols  []Column
	exprs []ir.Expr
	env   *Env
}

func (p *projectOp) OutputSchema() []Column { return p.cols }
func (p *projectOp) Close() error           { return p.in.Close() }

func (p *projectOp) Next(ctx context.Context) (Row, error) {
	r, err := p.in.Next(ctx)
	if err != nil {
		return nil, err
	}
	out := make(Row, len(p.exprs))
	for i, e := range p.exprs {
		v, err := evalExpr(e, r, p.env)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// --- Values ---

func buildValues(p *ir.Values, env *Env) (Operator, error) {
	cols := make([]Column, 0)
	if len(p.Rows) > 0 {
		for i, e := range p.Rows[0] {
			cols = append(cols, Column{Name: fmt.Sprintf("column%d", i+1), Type: e.Type()})
		}
	}
	return &valuesOp{rows: p.Rows, cols: cols, env: env}, nil
}

type valuesOp struct {
	rows [][]ir.Expr
	cols []Column
	env  *Env
	pos  int
}

func (v *valuesOp) OutputSchema() []Column { return v.cols }
func (v *valuesOp) Close() error           { return nil }

func (v *valuesOp) Next(_ context.Context) (Row, error) {
	if v.pos >= len(v.rows) {
		return nil, io.EOF
	}
	src := v.rows[v.pos]
	v.pos++
	out := make(Row, len(src))
	for i, e := range src {
		val, err := evalExpr(e, nil, v.env)
		if err != nil {
			return nil, err
		}
		out[i] = val
	}
	return out, nil
}

// --- Filter ---

func buildFilter(p *ir.Filter, env *Env) (Operator, error) {
	in, err := Build(p.Input, env)
	if err != nil {
		return nil, err
	}
	cond, err := resolveExpr(p.Cond, in.OutputSchema(), env)
	if err != nil {
		return nil, err
	}
	return &filterOp{in: in, cond: cond, env: env}, nil
}

type filterOp struct {
	in   Operator
	cond ir.Expr
	env  *Env
}

func (f *filterOp) OutputSchema() []Column { return f.in.OutputSchema() }
func (f *filterOp) Close() error           { return f.in.Close() }

func (f *filterOp) Next(ctx context.Context) (Row, error) {
	for {
		r, err := f.in.Next(ctx)
		if err != nil {
			return nil, err
		}
		v, err := evalExpr(f.cond, r, f.env)
		if err != nil {
			return nil, err
		}
		// SQL three-valued logic: NULL is not "true". A row is kept only
		// when the predicate evaluates to a Go bool that is true.
		if b, ok := v.(bool); ok && b {
			return r, nil
		}
	}
}

// --- Join ---

// buildJoin compiles a Join IR node into a nested-loop operator. The
// operator's output schema is the concatenation of left and right
// schemas, which preserves each side's Qualifier so downstream
// ColumnRef resolution can disambiguate same-named columns.
//
// Supported types: Inner, Left, Cross. M5 can swap the nested loop for
// a hash build when performance matters; the operator interface is
// unchanged.
func buildJoin(p *ir.Join, env *Env) (Operator, error) {
	switch p.Type {
	case ir.JoinInner, ir.JoinLeft, ir.JoinCross:
	default:
		return nil, fmt.Errorf("exec: join type %d not supported yet", p.Type)
	}
	left, err := Build(p.Left, env)
	if err != nil {
		return nil, err
	}
	right, err := Build(p.Right, env)
	if err != nil {
		left.Close()
		return nil, err
	}
	combined := append(append([]Column(nil), left.OutputSchema()...), right.OutputSchema()...)
	var cond ir.Expr
	if p.Cond != nil {
		cond, err = resolveExpr(p.Cond, combined, env)
		if err != nil {
			left.Close()
			right.Close()
			return nil, err
		}
	}
	return &joinOp{
		left:     left,
		right:    right,
		cond:     cond,
		cols:     combined,
		env:      env,
		joinType: p.Type,
		rightWid: len(right.OutputSchema()),
	}, nil
}

type joinOp struct {
	left     Operator
	right    Operator
	cond     ir.Expr
	cols     []Column
	env      *Env
	joinType ir.JoinType
	rightWid int // width of right's row, used to NULL-pad LEFT misses

	// Right side is materialized once and rewound per left row so a
	// child operator that's only good for a single Next pass (Scan,
	// Project, …) still works.
	rightRows []Row
	rightInit bool

	curLeft    Row
	rightAt    int
	curMatched bool // true if curLeft matched any right row (for LEFT)
}

func (j *joinOp) OutputSchema() []Column { return j.cols }
func (j *joinOp) Close() error {
	lerr := j.left.Close()
	rerr := j.right.Close()
	if lerr != nil {
		return lerr
	}
	return rerr
}

func (j *joinOp) Next(ctx context.Context) (Row, error) {
	if !j.rightInit {
		rows, err := drain(j.right)
		if err != nil {
			return nil, err
		}
		j.rightRows = rows
		j.rightInit = true
	}
	for {
		if j.curLeft == nil {
			next, err := j.left.Next(ctx)
			if err != nil {
				return nil, err
			}
			j.curLeft = next
			j.rightAt = 0
			j.curMatched = false
		}
		for j.rightAt < len(j.rightRows) {
			right := j.rightRows[j.rightAt]
			j.rightAt++
			combined := concatRows(j.curLeft, right)
			match, err := j.evalCond(combined)
			if err != nil {
				return nil, err
			}
			if match {
				j.curMatched = true
				return combined, nil
			}
		}
		// Right side exhausted for this left row.
		if j.joinType == ir.JoinLeft && !j.curMatched {
			padded := concatRows(j.curLeft, nullRow(j.rightWid))
			j.curLeft = nil
			return padded, nil
		}
		j.curLeft = nil
	}
}

func (j *joinOp) evalCond(row Row) (bool, error) {
	// CROSS or "missing ON" (treated as TRUE) emits unconditionally.
	if j.cond == nil {
		return true, nil
	}
	v, err := evalExpr(j.cond, row, j.env)
	if err != nil {
		return false, err
	}
	b, ok := v.(bool)
	return ok && b, nil
}

func concatRows(a, b Row) Row {
	out := make(Row, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	return out
}

func nullRow(width int) Row { return make(Row, width) }

// --- Sort ---

func buildSort(p *ir.Sort, env *Env) (Operator, error) {
	in, err := Build(p.Input, env)
	if err != nil {
		return nil, err
	}
	keys := make([]ir.SortKey, len(p.Keys))
	for i, k := range p.Keys {
		resolved, err := resolveExpr(k.Expr, in.OutputSchema(), env)
		if err != nil {
			return nil, err
		}
		keys[i] = ir.SortKey{Expr: resolved, Desc: k.Desc}
	}
	rows, err := drain(in)
	if err != nil {
		return nil, err
	}
	if err := sortRows(rows, keys, env); err != nil {
		return nil, err
	}
	return &materializedOp{cols: in.OutputSchema(), rows: rows}, nil
}

func sortRows(rows []Row, keys []ir.SortKey, env *Env) error {
	var sortErr error
	sort.SliceStable(rows, func(i, j int) bool {
		if sortErr != nil {
			return false
		}
		for _, k := range keys {
			a, err := evalExpr(k.Expr, rows[i], env)
			if err != nil {
				sortErr = err
				return false
			}
			b, err := evalExpr(k.Expr, rows[j], env)
			if err != nil {
				sortErr = err
				return false
			}
			cmp, err := compareValues(a, b)
			if err != nil {
				sortErr = err
				return false
			}
			if cmp == 0 {
				continue
			}
			if k.Desc {
				return cmp > 0
			}
			return cmp < 0
		}
		return false
	})
	return sortErr
}

// --- Limit ---
//
// LIMIT and OFFSET expressions are evaluated lazily on first Next, not
// at build time. Build is also called for Describe-only purposes (to
// learn the result schema) when params don't have real values bound
// yet, and we don't want that path to fail on `LIMIT $1`.

func buildLimit(p *ir.Limit, env *Env) (Operator, error) {
	in, err := Build(p.Input, env)
	if err != nil {
		return nil, err
	}
	count, err := resolveOrNil(p.Count, env)
	if err != nil {
		return nil, fmt.Errorf("LIMIT: %w", err)
	}
	offset, err := resolveOrNil(p.Offset, env)
	if err != nil {
		return nil, fmt.Errorf("OFFSET: %w", err)
	}
	return &limitOp{in: in, countExpr: count, offsetExpr: offset, env: env}, nil
}

func resolveOrNil(e ir.Expr, env *Env) (ir.Expr, error) {
	if e == nil {
		return nil, nil
	}
	return resolveExpr(e, nil, env)
}

type limitOp struct {
	in         Operator
	countExpr  ir.Expr
	offsetExpr ir.Expr
	env        *Env

	resolved bool
	limit    int64 // -1 = unbounded
	offset   int64
	skipped  int64
	emitted  int64
}

func (l *limitOp) OutputSchema() []Column { return l.in.OutputSchema() }
func (l *limitOp) Close() error           { return l.in.Close() }

func (l *limitOp) resolve() error {
	if l.resolved {
		return nil
	}
	l.limit = -1
	if l.countExpr != nil {
		v, err := evalExpr(l.countExpr, nil, l.env)
		if err != nil {
			return fmt.Errorf("LIMIT: %w", err)
		}
		n, err := toInt64(v)
		if err != nil {
			return fmt.Errorf("LIMIT: %w", err)
		}
		l.limit = n
	}
	if l.offsetExpr != nil {
		v, err := evalExpr(l.offsetExpr, nil, l.env)
		if err != nil {
			return fmt.Errorf("OFFSET: %w", err)
		}
		n, err := toInt64(v)
		if err != nil {
			return fmt.Errorf("OFFSET: %w", err)
		}
		l.offset = n
	}
	l.resolved = true
	return nil
}

func (l *limitOp) Next(ctx context.Context) (Row, error) {
	if err := l.resolve(); err != nil {
		return nil, err
	}
	for l.skipped < l.offset {
		if _, err := l.in.Next(ctx); err != nil {
			return nil, err
		}
		l.skipped++
	}
	if l.limit >= 0 && l.emitted >= l.limit {
		return nil, io.EOF
	}
	r, err := l.in.Next(ctx)
	if err != nil {
		return nil, err
	}
	l.emitted++
	return r, nil
}

// --- CreateTable ---

func buildCreateTable(p *ir.CreateTable, env *Env) Operator {
	return &ddlOp{tag: "CREATE TABLE", do: func() error {
		cols := make([]catalog.Column, len(p.Columns))
		var checks []catalog.Check
		for i, c := range p.Columns {
			cols[i] = catalog.Column{Name: c.Name, Type: c.Type, NotNull: c.NotNull, Unique: c.Unique, Auto: c.Auto}
			if c.References != nil {
				cols[i].References = catalog.ColumnRef{
					Table:    c.References.Table,
					Column:   c.References.Column,
					OnDelete: catalog.OnDeleteAction(c.References.OnDelete),
				}
			}
			if c.Check != nil {
				checks = append(checks, catalog.Check{
					Name: p.Name + "_" + c.Name + "_check",
					Expr: c.Check,
				})
			}
		}
		if err := env.Schema.CreateTable(catalog.Table{Name: p.Name, Columns: cols, Checks: checks}); err != nil {
			return err
		}
		env.Engine.CreateTable(p.Name, len(cols))
		return nil
	}}
}

func buildDropTable(p *ir.DropTable, env *Env) Operator {
	return &ddlOp{tag: "DROP TABLE", do: func() error {
		ok := env.Schema.DropTable(p.Name)
		if !ok && !p.IfExists {
			return &SQLError{Code: "42P01", Message: fmt.Sprintf("table %q does not exist", p.Name)}
		}
		env.Engine.DropTable(p.Name)
		return nil
	}}
}

// ddlOp runs its side effect once on first Next, then reports EOF. It
// produces no rows but makes the side effect observable through the
// usual operator drain loop.
type ddlOp struct {
	tag  string
	do   func() error
	done bool
}

func (d *ddlOp) OutputSchema() []Column { return nil }
func (d *ddlOp) Close() error           { return nil }

func (d *ddlOp) Next(_ context.Context) (Row, error) {
	if d.done {
		return nil, io.EOF
	}
	d.done = true
	if d.do != nil {
		if err := d.do(); err != nil {
			return nil, err
		}
	}
	return nil, io.EOF
}

// CommandTag exposes the side-effect operator's command tag (e.g.
// "CREATE TABLE", "INSERT 0 N"). The wire layer needs it to set
// CommandComplete correctly for statements that produce no rows.
func CommandTag(op Operator) (string, bool) {
	if d, ok := op.(*ddlOp); ok {
		return d.tag, true
	}
	if i, ok := op.(*insertOp); ok {
		return i.tag(), true
	}
	if d, ok := op.(*deleteOp); ok {
		return d.tag(), true
	}
	if u, ok := op.(*updateOp); ok {
		return u.tag(), true
	}
	return "", false
}

// --- Insert ---

func buildInsert(p *ir.Insert, env *Env) (Operator, error) {
	ct, ok := env.Schema.Table(p.Table)
	if !ok {
		return nil, fmt.Errorf("exec: unknown table %q", p.Table)
	}
	st, ok := env.Txn.Table(p.Table)
	if !ok {
		return nil, fmt.Errorf("exec: storage missing table %q", p.Table)
	}
	colMap, err := buildInsertColumnMap(ct, p.Columns)
	if err != nil {
		return nil, err
	}
	// Resolve each row's expressions against an empty input schema —
	// VALUES expressions don't see column refs.
	resolvedRows := make([][]ir.Expr, len(p.Rows))
	for i, row := range p.Rows {
		if len(row) != len(colMap) {
			return nil, fmt.Errorf("exec: insert row %d has %d values, want %d", i, len(row), len(colMap))
		}
		out := make([]ir.Expr, len(row))
		for j, e := range row {
			r, err := resolveExpr(e, nil, env)
			if err != nil {
				return nil, err
			}
			out[j] = r
		}
		resolvedRows[i] = out
	}
	op := &insertOp{
		table:  st,
		ct:     ct,
		colMap: colMap,
		rows:   resolvedRows,
		env:    env,
	}
	if len(p.Returning) > 0 {
		// RETURNING expressions see the post-INSERT row, so their column
		// refs resolve against the table's full schema (catalog order),
		// not the INSERT's column list.
		tableSchema := make([]Column, len(ct.Columns))
		for k, c := range ct.Columns {
			tableSchema[k] = Column{Name: c.Name, Type: c.Type}
		}
		op.returning = make([]ir.Expr, len(p.Returning))
		op.returningCols = make([]Column, len(p.Returning))
		for k, e := range p.Returning {
			r, err := resolveExpr(e, tableSchema, env)
			if err != nil {
				return nil, err
			}
			op.returning[k] = r
			name := ""
			if k < len(p.ReturningNames) {
				name = p.ReturningNames[k]
			}
			op.returningCols[k] = Column{Name: name, Type: r.Type()}
		}
	}
	return op, nil
}

// buildInsertColumnMap returns the mapping from VALUES-tuple position
// to table-column index. For "INSERT INTO t VALUES (...)" with no
// column list, the mapping is the identity over all columns.
func buildInsertColumnMap(ct catalog.Table, cols []string) ([]int, error) {
	if len(cols) == 0 {
		out := make([]int, len(ct.Columns))
		for i := range out {
			out[i] = i
		}
		return out, nil
	}
	out := make([]int, len(cols))
	for i, name := range cols {
		idx := -1
		for j, c := range ct.Columns {
			if c.Name == name {
				idx = j
				break
			}
		}
		if idx < 0 {
			return nil, fmt.Errorf("exec: insert into %q: unknown column %q", ct.Name, name)
		}
		out[i] = idx
	}
	return out, nil
}

type insertOp struct {
	table    storage.Table
	ct       catalog.Table
	colMap   []int
	rows     [][]ir.Expr
	env      *Env
	done     bool
	inserted int

	// Optional RETURNING projection. Non-nil iff the INSERT has a
	// RETURNING clause; in that case OutputSchema is non-empty and
	// Next emits one row per inserted row before EOF.
	returning     []ir.Expr
	returningCols []Column
	pending       []Row // computed during the side-effect pass
	pendingPos    int
}

func (i *insertOp) OutputSchema() []Column { return i.returningCols }
func (i *insertOp) Close() error           { return nil }

func (i *insertOp) Next(_ context.Context) (Row, error) {
	if !i.done {
		if err := i.runOnce(); err != nil {
			return nil, err
		}
	}
	if i.pendingPos < len(i.pending) {
		r := i.pending[i.pendingPos]
		i.pendingPos++
		return r, nil
	}
	return nil, io.EOF
}

// runOnce performs the side effect (build, validate, insert) and, if
// the INSERT carries a RETURNING clause, computes the projected output
// rows so subsequent Next calls can deliver them.
func (i *insertOp) runOnce() error {
	i.done = true
	// Build and validate every row before touching storage. The
	// transaction layer that would otherwise undo half-applied writes
	// is in place but the *operator* still owes all-or-nothing on
	// constraint failures within a single statement.
	autoCols := autoColumnIndexes(i.ct, i.colMap)
	built := make([]storage.Row, len(i.rows))
	for r, exprRow := range i.rows {
		row := make(storage.Row, len(i.ct.Columns))
		for j, e := range exprRow {
			v, err := evalExpr(e, nil, i.env)
			if err != nil {
				return err
			}
			row[i.colMap[j]] = v
		}
		fillAutoColumns(row, i.ct, autoCols, i.table)
		if err := checkNotNull(i.ct, row); err != nil {
			return err
		}
		built[r] = row
	}
	if err := checkUnique(i.ct, i.table.Rows(), built); err != nil {
		return err
	}
	if err := checkChecks(i.ct, built, i.env); err != nil {
		return err
	}
	if err := checkForeignKeys(i.ct, built, i.env); err != nil {
		return err
	}
	for _, row := range built {
		i.table.Insert(row)
		i.inserted++
	}
	if len(i.returning) > 0 {
		i.pending = make([]Row, len(built))
		for k, row := range built {
			out := make(Row, len(i.returning))
			for j, e := range i.returning {
				v, err := evalExpr(e, Row(row), i.env)
				if err != nil {
					return err
				}
				out[j] = v
			}
			i.pending[k] = out
		}
	}
	return nil
}

// autoColumnIndexes returns the catalog column indexes that carry the
// Auto flag *and* aren't named in the INSERT's column list — i.e. the
// columns the engine must fill itself. colMap is the INSERT-position →
// catalog-index mapping.
func autoColumnIndexes(ct catalog.Table, colMap []int) []int {
	mentioned := make(map[int]bool, len(colMap))
	for _, idx := range colMap {
		mentioned[idx] = true
	}
	var out []int
	for idx, c := range ct.Columns {
		if c.Auto && !mentioned[idx] {
			out = append(out, idx)
		}
	}
	return out
}

// fillAutoColumns writes the next sequence value into every Auto column
// slot the INSERT didn't supply. SERIAL → int32 to match the column
// type; BIGSERIAL → int64. The counter advances on the canonical table
// so two transactions never see the same value.
func fillAutoColumns(row storage.Row, ct catalog.Table, autoCols []int, tbl storage.Table) {
	for _, idx := range autoCols {
		next := tbl.NextAuto(idx)
		if ct.Columns[idx].Type == types.Int4 {
			row[idx] = int32(next)
		} else {
			row[idx] = next
		}
	}
}

// checkNotNull validates a fully-built insert row against the catalog's
// NOT NULL constraints. Catalog column order matches storage row order,
// so we walk them in lockstep.
func checkNotNull(ct catalog.Table, row storage.Row) error {
	for idx, col := range ct.Columns {
		if !col.NotNull {
			continue
		}
		if idx >= len(row) || row[idx] == nil {
			return NotNullViolation(ct.Name, col.Name)
		}
	}
	return nil
}

// checkForeignKeys validates each row's FK columns against their
// parent tables. NULL values are skipped — SQL leaves NULL FKs alone
// (it's the standard "match simple" behaviour). The parent lookup
// reads from the same txn so an in-tx INSERT into the parent is
// visible to a follow-up INSERT into the child.
func checkForeignKeys(ct catalog.Table, rows []storage.Row, env *Env) error {
	if env == nil {
		return nil
	}
	for colIdx, col := range ct.Columns {
		if col.References.Table == "" {
			continue
		}
		parentTable, ok := env.Schema.Table(col.References.Table)
		if !ok {
			return fmt.Errorf("exec: FK %s.%s references unknown table %q", ct.Name, col.Name, col.References.Table)
		}
		parentColIdx := -1
		for i, pc := range parentTable.Columns {
			if pc.Name == col.References.Column {
				parentColIdx = i
				break
			}
		}
		if parentColIdx < 0 {
			return fmt.Errorf("exec: FK %s.%s references unknown column %s.%s",
				ct.Name, col.Name, col.References.Table, col.References.Column)
		}
		parentRows, _ := env.Txn.Table(col.References.Table)
		var existing []storage.Row
		if parentRows != nil {
			existing = parentRows.Rows()
		}
		for _, row := range rows {
			if colIdx >= len(row) || row[colIdx] == nil {
				continue
			}
			if !rowExistsWithValue(existing, parentColIdx, row[colIdx]) {
				return FKViolationOnInsert(ct.Name, col.Name, col.References.Table)
			}
		}
	}
	return nil
}

// rowExistsWithValue is a linear scan — fine at M3 row counts.
// Replace with the btree we already promise on UNIQUE columns when
// performance starts to matter.
func rowExistsWithValue(rows []storage.Row, colIdx int, want any) bool {
	for _, r := range rows {
		if colIdx >= len(r) {
			continue
		}
		if r[colIdx] == nil {
			continue
		}
		cmp, err := compareValues(r[colIdx], want)
		if err == nil && cmp == 0 {
			return true
		}
	}
	return false
}

// applyDeleteCascades walks every other table that references parent
// and dispatches on each FK column's OnDelete action:
//   - RESTRICT (default): if any surviving child row points at a
//     deleted parent row, abort with SQLSTATE 23503.
//   - CASCADE: delete the matching child rows from their table. May
//     recursively cascade if those rows are themselves referenced.
//   - SET NULL: rewrite the child column to NULL on matching rows.
//
// All work goes through the txn snapshots so cascades roll back
// cleanly with the surrounding transaction.
func applyDeleteCascades(parent catalog.Table, deleted []storage.Row, env *Env) error {
	if env == nil || len(deleted) == 0 {
		return nil
	}
	parentColIdx := map[string]int{}
	for i, c := range parent.Columns {
		parentColIdx[c.Name] = i
	}
	for _, child := range env.Schema.Tables() {
		if child.Name == parent.Name {
			continue
		}
		for childColIdx, childCol := range child.Columns {
			if childCol.References.Table != parent.Name {
				continue
			}
			pIdx, ok := parentColIdx[childCol.References.Column]
			if !ok {
				continue
			}
			if err := applyChildAction(parent, deleted, pIdx, child, childCol, childColIdx, env); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyChildAction handles one (parent, child, childCol) triple. It
// scans child rows for matches against the deleted parent values and
// dispatches by action.
func applyChildAction(parent catalog.Table, deleted []storage.Row, pIdx int,
	child catalog.Table, childCol catalog.Column, childColIdx int, env *Env) error {
	childTbl, _ := env.Txn.Table(child.Name)
	if childTbl == nil {
		return nil
	}
	parentVals := collectNonNullValues(deleted, pIdx)
	if len(parentVals) == 0 {
		return nil
	}
	matches := matchingChildRowIndexes(childTbl.Rows(), childColIdx, parentVals)
	if len(matches) == 0 {
		return nil
	}
	switch childCol.References.OnDelete {
	case catalog.OnDeleteRestrict:
		return FKViolationOnDelete(parent.Name, child.Name)
	case catalog.OnDeleteCascade:
		return cascadeChildDelete(child, childTbl, matches, env)
	case catalog.OnDeleteSetNull:
		setChildColumnNull(childTbl, matches, childColIdx)
		return nil
	default:
		return fmt.Errorf("exec: unknown FK OnDelete action %d", childCol.References.OnDelete)
	}
}

func collectNonNullValues(rows []storage.Row, idx int) []any {
	var out []any
	for _, r := range rows {
		if idx >= len(r) || r[idx] == nil {
			continue
		}
		out = append(out, r[idx])
	}
	return out
}

func matchingChildRowIndexes(rows []storage.Row, colIdx int, vals []any) []int {
	var out []int
	for i, r := range rows {
		if colIdx >= len(r) || r[colIdx] == nil {
			continue
		}
		for _, v := range vals {
			if cmp, err := compareValues(r[colIdx], v); err == nil && cmp == 0 {
				out = append(out, i)
				break
			}
		}
	}
	return out
}

// cascadeChildDelete drops the matched rows from the child table and
// recursively applies cascades to anything that referenced *them*.
func cascadeChildDelete(child catalog.Table, childTbl storage.Table, drop []int, env *Env) error {
	dropSet := make(map[int]bool, len(drop))
	for _, i := range drop {
		dropSet[i] = true
	}
	var removed []storage.Row
	childTbl.Mutate(func(rows []storage.Row) []storage.Row {
		kept := make([]storage.Row, 0, len(rows))
		for i, r := range rows {
			if dropSet[i] {
				removed = append(removed, r)
				continue
			}
			kept = append(kept, r)
		}
		return kept
	})
	return applyDeleteCascades(child, removed, env)
}

// setChildColumnNull rewrites colIdx to nil on each matched row. NULL
// on a NOT NULL column would be caught by a re-validation pass, but we
// rely on the user to have declared the FK column nullable when they
// chose SET NULL — matching PG's runtime error in that misconfigured
// case is a follow-up.
func setChildColumnNull(childTbl storage.Table, matches []int, colIdx int) {
	matchSet := make(map[int]bool, len(matches))
	for _, i := range matches {
		matchSet[i] = true
	}
	childTbl.Mutate(func(rows []storage.Row) []storage.Row {
		for i := range rows {
			if !matchSet[i] {
				continue
			}
			if colIdx < len(rows[i]) {
				rows[i][colIdx] = nil
			}
		}
		return rows
	})
}

// checkChecks evaluates each CHECK constraint against every incoming
// row. CHECKs may reference columns of the same row, so we resolve the
// expression once against a synthetic schema built from the catalog,
// then re-use the resolved expression across all rows in the batch.
//
// Per real PG: a CHECK that evaluates to NULL is treated as success
// (only an explicit FALSE rejects). Matches sqlc-generated test code
// expectations.
func checkChecks(ct catalog.Table, rows []storage.Row, env *Env) error {
	if len(ct.Checks) == 0 {
		return nil
	}
	schema := make([]Column, len(ct.Columns))
	for i, c := range ct.Columns {
		schema[i] = Column{Name: c.Name, Type: c.Type}
	}
	for _, chk := range ct.Checks {
		resolved, err := resolveExpr(chk.Expr, schema, env)
		if err != nil {
			return err
		}
		for _, row := range rows {
			v, err := evalExpr(resolved, Row(row), env)
			if err != nil {
				return err
			}
			if b, ok := v.(bool); ok && !b {
				return CheckViolation(ct.Name, chk.Name)
			}
		}
	}
	return nil
}

// checkUnique enforces single-column UNIQUE constraints. We rebuild the
// per-column value sets from existing rows on every insert — DESIGN.md
// §3 explicitly accepts O(n) scans for correctness in M3. NULLs are
// not considered equal (real PG semantics), so multiple NULLs in a
// unique column are allowed.
func checkUnique(ct catalog.Table, existing, incoming []storage.Row) error {
	for idx, col := range ct.Columns {
		if !col.Unique {
			continue
		}
		// Map keys must be comparable, so we route through uniqueKey
		// which converts non-comparable types ([]byte) to a string. The
		// type prefix prevents cross-type collisions.
		seen := map[string]struct{}{}
		for _, r := range existing {
			if idx >= len(r) || r[idx] == nil {
				continue
			}
			seen[uniqueKey(r[idx])] = struct{}{}
		}
		for _, r := range incoming {
			if idx >= len(r) || r[idx] == nil {
				continue
			}
			k := uniqueKey(r[idx])
			if _, dup := seen[k]; dup {
				return UniqueViolation(ct.Name, col.Name)
			}
			seen[k] = struct{}{}
		}
	}
	return nil
}

// uniqueKey turns a row value into a string usable as a map key. The
// type prefix keeps int32(1) and int64(1) distinct (we shouldn't see
// mixed types within one column today, but the prefix is cheap
// insurance). For bytea ([]byte) we hex-encode rather than convert
// directly — strings and []byte with the same bytes would otherwise
// collide if we ever land arbitrary-typed columns.
func uniqueKey(v any) string {
	switch x := v.(type) {
	case []byte:
		return "bytea:" + string(x)
	case string:
		return "text:" + x
	case int32:
		return fmt.Sprintf("int4:%d", x)
	case int64:
		return fmt.Sprintf("int8:%d", x)
	case bool:
		if x {
			return "bool:t"
		}
		return "bool:f"
	case [16]byte:
		return "uuid:" + string(x[:])
	case time.Time:
		return "ts:" + x.UTC().Format(time.RFC3339Nano)
	default:
		return fmt.Sprintf("%T:%v", v, v)
	}
}

func (i *insertOp) tag() string { return fmt.Sprintf("INSERT 0 %d", i.inserted) }

// --- Delete ---

func buildDelete(p *ir.Delete, env *Env) (Operator, error) {
	ct, ok := env.Schema.Table(p.Table)
	if !ok {
		return nil, fmt.Errorf("exec: unknown table %q", p.Table)
	}
	st, ok := env.Txn.Table(p.Table)
	if !ok {
		return nil, fmt.Errorf("exec: storage missing table %q", p.Table)
	}
	tableSchema := make([]Column, len(ct.Columns))
	for i, c := range ct.Columns {
		tableSchema[i] = Column{Name: c.Name, Type: c.Type}
	}
	op := &deleteOp{table: st, ct: ct, env: env, tableSchema: tableSchema}
	if p.Where != nil {
		cond, err := resolveExpr(p.Where, tableSchema, env)
		if err != nil {
			return nil, err
		}
		op.where = cond
	}
	if len(p.Returning) > 0 {
		op.returning = make([]ir.Expr, len(p.Returning))
		op.returningCols = make([]Column, len(p.Returning))
		for k, e := range p.Returning {
			r, err := resolveExpr(e, tableSchema, env)
			if err != nil {
				return nil, err
			}
			op.returning[k] = r
			name := ""
			if k < len(p.ReturningNames) {
				name = p.ReturningNames[k]
			}
			op.returningCols[k] = Column{Name: name, Type: r.Type()}
		}
	}
	return op, nil
}

type deleteOp struct {
	table       storage.Table
	ct          catalog.Table
	tableSchema []Column
	where       ir.Expr // nil → delete all
	env         *Env

	done    bool
	deleted int

	returning     []ir.Expr
	returningCols []Column
	pending       []Row
	pendingPos    int
}

func (d *deleteOp) OutputSchema() []Column { return d.returningCols }
func (d *deleteOp) Close() error           { return nil }

func (d *deleteOp) Next(_ context.Context) (Row, error) {
	if !d.done {
		if err := d.runOnce(); err != nil {
			return nil, err
		}
	}
	if d.pendingPos < len(d.pending) {
		r := d.pending[d.pendingPos]
		d.pendingPos++
		return r, nil
	}
	return nil, io.EOF
}

func (d *deleteOp) runOnce() error {
	d.done = true
	// Mutate locks the table for the duration of the predicate walk so
	// concurrent inserts can't slip in between read and write. We keep
	// matching rows in a local buffer for RETURNING projection. If the
	// predicate errors mid-walk, we leave the table untouched and
	// surface the error — partial deletes would be confusing.
	var deleted []storage.Row
	var evalErr error
	d.table.Mutate(func(rows []storage.Row) []storage.Row {
		kept := make([]storage.Row, 0, len(rows))
		for _, row := range rows {
			match, err := d.matches(row)
			if err != nil {
				evalErr = err
				deleted = nil // throw away anything we'd queued
				return rows   // table stays exactly as it was
			}
			if match {
				deleted = append(deleted, row)
			} else {
				kept = append(kept, row)
			}
		}
		// FK enforcement: RESTRICT aborts, CASCADE recursively deletes
		// child rows, SET NULL nulls out the FK column on dependents.
		if err := applyDeleteCascades(d.ct, deleted, d.env); err != nil {
			evalErr = err
			deleted = nil
			return rows
		}
		return kept
	})
	if evalErr != nil {
		return evalErr
	}
	d.deleted = len(deleted)
	if len(d.returning) > 0 {
		d.pending = make([]Row, len(deleted))
		for i, row := range deleted {
			out := make(Row, len(d.returning))
			for j, e := range d.returning {
				v, err := evalExpr(e, Row(row), d.env)
				if err != nil {
					return err
				}
				out[j] = v
			}
			d.pending[i] = out
		}
	}
	return nil
}

func (d *deleteOp) matches(row storage.Row) (bool, error) {
	if d.where == nil {
		return true, nil
	}
	v, err := evalExpr(d.where, Row(row), d.env)
	if err != nil {
		return false, err
	}
	// SQL three-valued logic: NULL is not "true", so it doesn't match.
	b, ok := v.(bool)
	return ok && b, nil
}

func (d *deleteOp) tag() string { return fmt.Sprintf("DELETE %d", d.deleted) }

// --- Update ---

func buildUpdate(p *ir.Update, env *Env) (Operator, error) {
	ct, ok := env.Schema.Table(p.Table)
	if !ok {
		return nil, fmt.Errorf("exec: unknown table %q", p.Table)
	}
	st, ok := env.Txn.Table(p.Table)
	if !ok {
		return nil, fmt.Errorf("exec: storage missing table %q", p.Table)
	}
	tableSchema := make([]Column, len(ct.Columns))
	for i, c := range ct.Columns {
		tableSchema[i] = Column{Name: c.Name, Type: c.Type}
	}
	op := &updateOp{table: st, ct: ct, tableSchema: tableSchema, env: env}

	op.assigns = make([]resolvedAssign, len(p.Assignments))
	for i, a := range p.Assignments {
		colIdx := -1
		for j, c := range ct.Columns {
			if c.Name == a.Column {
				colIdx = j
				break
			}
		}
		if colIdx < 0 {
			return nil, fmt.Errorf("exec: update %q: unknown column %q", p.Table, a.Column)
		}
		expr, err := resolveExpr(a.Expr, tableSchema, env)
		if err != nil {
			return nil, err
		}
		op.assigns[i] = resolvedAssign{colIdx: colIdx, expr: expr}
	}

	if p.Where != nil {
		cond, err := resolveExpr(p.Where, tableSchema, env)
		if err != nil {
			return nil, err
		}
		op.where = cond
	}

	if len(p.Returning) > 0 {
		op.returning = make([]ir.Expr, len(p.Returning))
		op.returningCols = make([]Column, len(p.Returning))
		for k, e := range p.Returning {
			r, err := resolveExpr(e, tableSchema, env)
			if err != nil {
				return nil, err
			}
			op.returning[k] = r
			name := ""
			if k < len(p.ReturningNames) {
				name = p.ReturningNames[k]
			}
			op.returningCols[k] = Column{Name: name, Type: r.Type()}
		}
	}
	return op, nil
}

type resolvedAssign struct {
	colIdx int
	expr   ir.Expr
}

type updateOp struct {
	table       storage.Table
	ct          catalog.Table
	tableSchema []Column
	assigns     []resolvedAssign
	where       ir.Expr
	env         *Env

	done    bool
	updated int

	returning     []ir.Expr
	returningCols []Column
	pending       []Row
	pendingPos    int
}

func (u *updateOp) OutputSchema() []Column { return u.returningCols }
func (u *updateOp) Close() error           { return nil }

func (u *updateOp) Next(_ context.Context) (Row, error) {
	if !u.done {
		if err := u.runOnce(); err != nil {
			return nil, err
		}
	}
	if u.pendingPos < len(u.pending) {
		r := u.pending[u.pendingPos]
		u.pendingPos++
		return r, nil
	}
	return nil, io.EOF
}

func (u *updateOp) runOnce() error {
	u.done = true
	var (
		evalErr     error
		updatedRows []storage.Row // freshly-updated rows (for RETURNING)
	)
	u.table.Mutate(func(rows []storage.Row) []storage.Row {
		next := make([]storage.Row, len(rows))
		for i, row := range rows {
			match, err := u.matches(row)
			if err != nil {
				evalErr = err
				return rows
			}
			if !match {
				next[i] = row
				continue
			}
			updated, err := u.applyAssignments(row)
			if err != nil {
				evalErr = err
				return rows
			}
			if err := checkNotNull(u.ct, updated); err != nil {
				evalErr = err
				return rows
			}
			next[i] = updated
			updatedRows = append(updatedRows, updated)
		}
		// Validate the post-update table as a whole: UNIQUE across all
		// rows (existing+updated), CHECK against the new rows.
		if err := checkUnique(u.ct, nil, next); err != nil {
			evalErr = err
			return rows
		}
		if err := checkChecks(u.ct, updatedRows, u.env); err != nil {
			evalErr = err
			return rows
		}
		if err := checkForeignKeys(u.ct, updatedRows, u.env); err != nil {
			evalErr = err
			return rows
		}
		return next
	})
	if evalErr != nil {
		return evalErr
	}
	u.updated = len(updatedRows)
	if len(u.returning) > 0 {
		u.pending = make([]Row, len(updatedRows))
		for i, row := range updatedRows {
			out := make(Row, len(u.returning))
			for j, e := range u.returning {
				v, err := evalExpr(e, Row(row), u.env)
				if err != nil {
					return err
				}
				out[j] = v
			}
			u.pending[i] = out
		}
	}
	return nil
}

func (u *updateOp) matches(row storage.Row) (bool, error) {
	if u.where == nil {
		return true, nil
	}
	v, err := evalExpr(u.where, Row(row), u.env)
	if err != nil {
		return false, err
	}
	b, ok := v.(bool)
	return ok && b, nil
}

// applyAssignments returns a new row with each assignment evaluated
// against the *original* row. PG semantics: assignments don't see each
// other's effects within the same UPDATE.
func (u *updateOp) applyAssignments(orig storage.Row) (storage.Row, error) {
	out := append(storage.Row(nil), orig...)
	for _, a := range u.assigns {
		v, err := evalExpr(a.expr, Row(orig), u.env)
		if err != nil {
			return nil, err
		}
		out[a.colIdx] = v
	}
	return out, nil
}

func (u *updateOp) tag() string { return fmt.Sprintf("UPDATE %d", u.updated) }

// --- helpers ---

// materializedOp is a reusable in-memory operator. Sort uses it to
// deliver rows in the requested order without each downstream operator
// having to know it was sorted.
type materializedOp struct {
	cols []Column
	rows []Row
	pos  int
}

func (m *materializedOp) OutputSchema() []Column { return m.cols }
func (m *materializedOp) Close() error           { return nil }
func (m *materializedOp) Next(_ context.Context) (Row, error) {
	if m.pos >= len(m.rows) {
		return nil, io.EOF
	}
	r := m.rows[m.pos]
	m.pos++
	return r, nil
}

func drain(op Operator) ([]Row, error) {
	var out []Row
	for {
		r, err := op.Next(context.Background())
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
}

// resolveColumnRef finds the slot in schema that ref names. With a
// qualifier we require both Qualifier and Name to match. Without a
// qualifier we name-match alone — and if more than one column matches
// (joined tables both have an `id`, say) we error like real PG.
func resolveColumnRef(ref *ir.ColumnRef, schema []Column) (int, error) {
	matches := make([]int, 0, 2)
	for i, c := range schema {
		if ref.Name != c.Name {
			continue
		}
		if ref.Qualifier != "" && ref.Qualifier != c.Qualifier {
			continue
		}
		matches = append(matches, i)
	}
	if len(matches) == 0 {
		return 0, fmt.Errorf("exec: unknown column %s", refDisplayName(ref))
	}
	if len(matches) > 1 {
		return 0, fmt.Errorf("exec: column reference %q is ambiguous", ref.Name)
	}
	return matches[0], nil
}

func refDisplayName(ref *ir.ColumnRef) string {
	if ref.Qualifier != "" {
		return fmt.Sprintf("%q.%q", ref.Qualifier, ref.Name)
	}
	return fmt.Sprintf("%q", ref.Name)
}

// resolveExpr recursively fills in ColumnRef.Index/T (from the input
// schema) and ParamRef.T (from the bound parameter list). Pure literals
// pass through unchanged.
// resolveExpr fills in static metadata on an expression tree:
//   - ColumnRef gets Index + T from the input schema
//   - ParamRef gets T from env.Params
//   - FuncCall gets a result type from the builtin registry
//   - Sub-queries (uncorrelated) are *evaluated* here against env and
//     replaced with literals — that's why env carries Engine/Schema/Txn,
//     not just Params.
//
// env may be nil when called from a context that has no engine handle
// (CHECK constraints don't need one, parameters don't appear there).
// In that case, a ParamRef or subquery in e errors loudly.
func resolveExpr(e ir.Expr, schema []Column, env *Env) (ir.Expr, error) {
	switch x := e.(type) {
	case *ir.Literal:
		return x, nil
	case *ir.ColumnRef:
		idx, err := resolveColumnRef(x, schema)
		if err != nil {
			return nil, err
		}
		return &ir.ColumnRef{Qualifier: x.Qualifier, Name: x.Name, Index: idx, T: schema[idx].Type}, nil
	case *ir.ParamRef:
		params := envParams(env)
		if x.Index < 0 || x.Index >= len(params) {
			return nil, fmt.Errorf("exec: $%d not bound (%d params provided)", x.Index+1, len(params))
		}
		return &ir.ParamRef{Index: x.Index, T: params[x.Index].Type}, nil
	case *ir.BinOp:
		l, err := resolveExpr(x.Left, schema, env)
		if err != nil {
			return nil, err
		}
		r, err := resolveExpr(x.Right, schema, env)
		if err != nil {
			return nil, err
		}
		t := x.T
		if t == nil {
			t = arithResultType(l.Type(), r.Type())
		}
		return &ir.BinOp{Op: x.Op, Left: l, Right: r, T: t}, nil
	case *ir.UnaryOp:
		inner, err := resolveExpr(x.Expr, schema, env)
		if err != nil {
			return nil, err
		}
		t := x.T
		if t == nil {
			// Falls back to the resolved inner's type — matters for
			// unary minus over expressions whose own type isn't fixed
			// at parse time (e.g. `-(a + b)` where the BinOp's T is
			// only filled by arithResultType during resolution).
			t = inner.Type()
		}
		return &ir.UnaryOp{Op: x.Op, Expr: inner, T: t}, nil
	case *ir.FuncCall:
		args := make([]ir.Expr, len(x.Args))
		for i, a := range x.Args {
			r, err := resolveExpr(a, schema, env)
			if err != nil {
				return nil, err
			}
			args[i] = r
		}
		fn, err := lookupBuiltin(x.Name)
		if err != nil {
			return nil, err
		}
		t, err := fn.ResultType(args)
		if err != nil {
			return nil, fmt.Errorf("function %q: %w", x.Name, err)
		}
		return &ir.FuncCall{Name: x.Name, Args: args, T: t}, nil
	case *ir.ScalarSubquery:
		return evalScalarSubquery(x, env)
	case *ir.InListExpr:
		probe, err := resolveExpr(x.Probe, schema, env)
		if err != nil {
			return nil, err
		}
		list := make([]ir.Expr, len(x.List))
		for i, e := range x.List {
			r, err := resolveExpr(e, schema, env)
			if err != nil {
				return nil, err
			}
			list[i] = r
		}
		return &ir.InListExpr{Probe: probe, List: list}, nil
	case *ir.InSubqueryExpr:
		probe, err := resolveExpr(x.Probe, schema, env)
		if err != nil {
			return nil, err
		}
		list, err := evalInSubquery(x, env)
		if err != nil {
			return nil, err
		}
		return &ir.InListExpr{Probe: probe, List: list}, nil
	case *ir.Cast:
		inner, err := resolveExpr(x.Expr, schema, env)
		if err != nil {
			return nil, err
		}
		return &ir.Cast{Expr: inner, T: x.T}, nil
	default:
		return nil, fmt.Errorf("exec: unsupported expr %T", e)
	}
}

func envParams(env *Env) []Param {
	if env == nil {
		return nil
	}
	return env.Params
}

// evalScalarSubquery runs the inner plan against env and returns a
// Literal carrying the single (column 0, row 0) value. More than one
// row is SQLSTATE 21000 ("more than one row returned by a subquery
// used as an expression").
func evalScalarSubquery(s *ir.ScalarSubquery, env *Env) (ir.Expr, error) {
	if env == nil {
		return nil, fmt.Errorf("exec: scalar subquery requires execution environment")
	}
	op, err := Build(s.Plan, env)
	if err != nil {
		return nil, err
	}
	defer op.Close()
	cols := op.OutputSchema()
	if len(cols) != 1 {
		return nil, fmt.Errorf("exec: scalar subquery returned %d columns, want 1", len(cols))
	}
	row, err := op.Next(context.Background())
	if errors.Is(err, io.EOF) {
		return &ir.Literal{Value: nil, T: cols[0].Type}, nil
	}
	if err != nil {
		return nil, err
	}
	value := any(nil)
	if len(row) > 0 {
		value = row[0]
	}
	if _, err := op.Next(context.Background()); err == nil {
		return nil, &SQLError{Code: "21000", Message: "more than one row returned by a subquery used as an expression"}
	} else if !errors.Is(err, io.EOF) {
		return nil, err
	}
	return &ir.Literal{Value: value, T: cols[0].Type}, nil
}

// evalInSubquery runs the inner plan and returns its first column's
// values as a list of Literal expressions, ready to feed an InListExpr.
func evalInSubquery(s *ir.InSubqueryExpr, env *Env) ([]ir.Expr, error) {
	if env == nil {
		return nil, fmt.Errorf("exec: IN subquery requires execution environment")
	}
	op, err := Build(s.Plan, env)
	if err != nil {
		return nil, err
	}
	defer op.Close()
	cols := op.OutputSchema()
	if len(cols) != 1 {
		return nil, fmt.Errorf("exec: IN subquery returned %d columns, want 1", len(cols))
	}
	var out []ir.Expr
	for {
		row, err := op.Next(context.Background())
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		var v any
		if len(row) > 0 {
			v = row[0]
		}
		out = append(out, &ir.Literal{Value: v, T: cols[0].Type})
	}
}

func evalExpr(e ir.Expr, in Row, env *Env) (any, error) {
	switch x := e.(type) {
	case *ir.Literal:
		return x.Value, nil
	case *ir.ColumnRef:
		if x.Index < 0 || x.Index >= len(in) {
			return nil, fmt.Errorf("exec: column ref %q (idx %d) out of range (row width %d)", x.Name, x.Index, len(in))
		}
		return in[x.Index], nil
	case *ir.ParamRef:
		params := envParams(env)
		if x.Index < 0 || x.Index >= len(params) {
			return nil, fmt.Errorf("exec: $%d not bound", x.Index+1)
		}
		return params[x.Index].Value, nil
	case *ir.BinOp:
		return evalBinOp(x, in, env)
	case *ir.UnaryOp:
		return evalUnaryOp(x, in, env)
	case *ir.FuncCall:
		return evalFuncCall(x, in, env)
	case *ir.InListExpr:
		return evalInList(x, in, env)
	case *ir.Cast:
		v, err := evalExpr(x.Expr, in, env)
		if err != nil {
			return nil, err
		}
		return castValue(v, x.T)
	default:
		return nil, fmt.Errorf("exec: unsupported expr %T", e)
	}
}

// castValue implements the small slice of PG's cast lattice we care
// about: integer ⟷ text, integer widening / narrowing, bool ⟷ text,
// text → uuid, text → bytea (the `\xHEX` form), and any-type → itself
// (no-op when already the target type). NULL → NULL across the board.
//
// Unsupported casts surface as exec errors so the wire layer reports
// them rather than silently producing the wrong value.
func castValue(v any, target types.Type) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch target {
	case types.Text:
		return castToText(v)
	case types.Int4:
		return castToInt4(v)
	case types.Int8:
		return castToInt8(v)
	case types.Bool:
		return castToBool(v)
	case types.UUID:
		return castToUUID(v)
	case types.Bytea:
		return castToBytea(v)
	case types.Timestamptz:
		// Already a time.Time? Pass through. From text? Decode via the
		// type's own DecodeText. Anything else fails.
		if t, ok := v.(time.Time); ok {
			return t, nil
		}
		if s, ok := v.(string); ok {
			return types.Timestamptz.DecodeText([]byte(s))
		}
		return nil, fmt.Errorf("cast to timestamptz: unsupported source %T", v)
	case types.JSONB:
		// Same shape as bytea — JSON bytes pass through.
		return castToBytea(v)
	default:
		return nil, fmt.Errorf("cast to %s: unsupported", target.Name())
	}
}

func castToText(v any) (any, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case int32:
		return strconv.FormatInt(int64(x), 10), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case bool:
		if x {
			return "true", nil
		}
		return "false", nil
	case [16]byte:
		out, _ := types.UUID.EncodeText(x)
		return string(out), nil
	case []byte:
		out, _ := types.Bytea.EncodeText(x)
		return string(out), nil
	case time.Time:
		out, _ := types.Timestamptz.EncodeText(x)
		return string(out), nil
	default:
		return nil, fmt.Errorf("cast to text: unsupported source %T", v)
	}
}

func castToInt4(v any) (any, error) {
	switch x := v.(type) {
	case int32:
		return x, nil
	case int64:
		return int32(x), nil
	case string:
		n, err := strconv.ParseInt(x, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("cast to int4: %w", err)
		}
		return int32(n), nil
	case bool:
		if x {
			return int32(1), nil
		}
		return int32(0), nil
	default:
		return nil, fmt.Errorf("cast to int4: unsupported source %T", v)
	}
}

func castToInt8(v any) (any, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case int32:
		return int64(x), nil
	case string:
		n, err := strconv.ParseInt(x, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("cast to int8: %w", err)
		}
		return n, nil
	case bool:
		if x {
			return int64(1), nil
		}
		return int64(0), nil
	default:
		return nil, fmt.Errorf("cast to int8: unsupported source %T", v)
	}
}

func castToBool(v any) (any, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case string:
		return types.Bool.DecodeText([]byte(x))
	case int32:
		return x != 0, nil
	case int64:
		return x != 0, nil
	default:
		return nil, fmt.Errorf("cast to bool: unsupported source %T", v)
	}
}

func castToUUID(v any) (any, error) {
	switch x := v.(type) {
	case [16]byte:
		return x, nil
	case string:
		return types.UUID.DecodeText([]byte(x))
	case []byte:
		return types.UUID.DecodeBinary(x)
	default:
		return nil, fmt.Errorf("cast to uuid: unsupported source %T", v)
	}
}

func castToBytea(v any) (any, error) {
	switch x := v.(type) {
	case []byte:
		return x, nil
	case string:
		// Accept both the canonical \xHEX form and arbitrary text
		// (interpret raw UTF-8 bytes). The wire-text form gets
		// normalized via DecodeText; everything else falls through.
		if len(x) >= 2 && x[0] == '\\' && (x[1] == 'x' || x[1] == 'X') {
			return types.Bytea.DecodeText([]byte(x))
		}
		return []byte(x), nil
	default:
		return nil, fmt.Errorf("cast to bytea: unsupported source %T", v)
	}
}

// evalInList: SQL three-valued IN. NULL probe ⇒ NULL. Probe equal to
// any non-NULL list value ⇒ TRUE. Probe not equal to any non-NULL value,
// but at least one NULL in list ⇒ NULL. Otherwise FALSE.
func evalInList(x *ir.InListExpr, in Row, env *Env) (any, error) {
	probe, err := evalExpr(x.Probe, in, env)
	if err != nil {
		return nil, err
	}
	if probe == nil {
		return nil, nil
	}
	sawNull := false
	for _, e := range x.List {
		v, err := evalExpr(e, in, env)
		if err != nil {
			return nil, err
		}
		if v == nil {
			sawNull = true
			continue
		}
		cmp, err := compareValues(probe, v)
		if err != nil {
			return nil, err
		}
		if cmp == 0 {
			return true, nil
		}
	}
	if sawNull {
		return nil, nil
	}
	return false, nil
}

// evalFuncCall looks the builtin up by name, evaluates each argument
// against the current row, and dispatches to the registered impl.
// Errors from the impl bubble up unchanged so SQLError-typed failures
// (which there aren't any of yet) reach the wire layer.
func evalFuncCall(f *ir.FuncCall, in Row, env *Env) (any, error) {
	fn, err := lookupBuiltin(f.Name)
	if err != nil {
		return nil, err
	}
	values := make([]any, len(f.Args))
	for i, a := range f.Args {
		v, err := evalExpr(a, in, env)
		if err != nil {
			return nil, err
		}
		values[i] = v
	}
	return fn.Eval(env, values)
}

func evalBinOp(b *ir.BinOp, in Row, env *Env) (any, error) {
	switch b.Op {
	case "and":
		return evalAnd(b, in, env)
	case "or":
		return evalOr(b, in, env)
	}
	l, err := evalExpr(b.Left, in, env)
	if err != nil {
		return nil, err
	}
	r, err := evalExpr(b.Right, in, env)
	if err != nil {
		return nil, err
	}
	if l == nil || r == nil {
		return nil, nil
	}
	switch b.Op {
	case "+", "-", "*", "/", "%":
		return evalArith(b.Op, l, r, b.T)
	case "||":
		return evalConcat(l, r)
	}
	cmp, err := compareValues(l, r)
	if err != nil {
		return nil, err
	}
	switch b.Op {
	case "=":
		return cmp == 0, nil
	case "!=":
		return cmp != 0, nil
	case "<":
		return cmp < 0, nil
	case ">":
		return cmp > 0, nil
	case "<=":
		return cmp <= 0, nil
	case ">=":
		return cmp >= 0, nil
	default:
		return nil, fmt.Errorf("exec: unsupported binary op %q", b.Op)
	}
}

// arithResultType picks the output type of a binary additive/
// multiplicative op based on its operands.
//   - || is text concatenation; result type is text.
//   - either side int8 (BIGINT) → int8.
//   - otherwise → int4.
//
// Unknown operand types fall back to int4; if they aren't really
// integers evalArith rejects them at evaluation time.
func arithResultType(l, r types.Type) types.Type {
	if l == types.Text || r == types.Text {
		return types.Text
	}
	if l == types.Int8 || r == types.Int8 {
		return types.Int8
	}
	return types.Int4
}

// evalConcat is `text || text`. Either side NULL was filtered upstream.
// We accept any value that has a SQL-printable Go shape via fmt.Sprint
// so `'count: ' || n` (text || int) works the way PG implicitly casts
// arguments to text in this position.
func evalConcat(l, r any) (any, error) {
	return concatString(l) + concatString(r), nil
}

func concatString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case int64:
		return strconv.FormatInt(x, 10)
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(v)
	}
}

// evalArith does integer arithmetic in int64-space, then narrows to
// int32 if the static result type says int4. Division by zero matches
// PG behaviour: SQLSTATE 22012.
func evalArith(op string, l, r any, resultT types.Type) (any, error) {
	li, err := toInt64(l)
	if err != nil {
		return nil, err
	}
	ri, err := toInt64(r)
	if err != nil {
		return nil, err
	}
	var out int64
	switch op {
	case "+":
		out = li + ri
	case "-":
		out = li - ri
	case "*":
		out = li * ri
	case "/":
		if ri == 0 {
			return nil, &SQLError{Code: "22012", Message: "division by zero"}
		}
		out = li / ri
	case "%":
		if ri == 0 {
			return nil, &SQLError{Code: "22012", Message: "division by zero"}
		}
		out = li % ri
	default:
		return nil, fmt.Errorf("exec: unsupported arith op %q", op)
	}
	if resultT == types.Int4 {
		return int32(out), nil
	}
	return out, nil
}

func evalAnd(b *ir.BinOp, in Row, env *Env) (any, error) {
	lv, err := evalExpr(b.Left, in, env)
	if err != nil {
		return nil, err
	}
	if lb, ok := lv.(bool); ok && !lb {
		return false, nil
	}
	rv, err := evalExpr(b.Right, in, env)
	if err != nil {
		return nil, err
	}
	if rb, ok := rv.(bool); ok && !rb {
		return false, nil
	}
	if lv == nil || rv == nil {
		return nil, nil
	}
	return true, nil
}

func evalOr(b *ir.BinOp, in Row, env *Env) (any, error) {
	lv, err := evalExpr(b.Left, in, env)
	if err != nil {
		return nil, err
	}
	if lb, ok := lv.(bool); ok && lb {
		return true, nil
	}
	rv, err := evalExpr(b.Right, in, env)
	if err != nil {
		return nil, err
	}
	if rb, ok := rv.(bool); ok && rb {
		return true, nil
	}
	if lv == nil || rv == nil {
		return nil, nil
	}
	return false, nil
}

func evalUnaryOp(u *ir.UnaryOp, in Row, env *Env) (any, error) {
	v, err := evalExpr(u.Expr, in, env)
	if err != nil {
		return nil, err
	}
	switch u.Op {
	case "not":
		if v == nil {
			return nil, nil
		}
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("exec: NOT on non-bool %T", v)
		}
		return !b, nil
	case "-":
		if v == nil {
			return nil, nil
		}
		switch n := v.(type) {
		case int32:
			return -n, nil
		case int64:
			return -n, nil
		case int:
			return -int64(n), nil
		default:
			return nil, fmt.Errorf("exec: unary - on non-integer %T", v)
		}
	default:
		return nil, fmt.Errorf("exec: unsupported unary op %q", u.Op)
	}
}

// compareValues returns -1/0/1 for two values of the same logical type.
// We compare on Go's native ordering for the integer/text types M2
// supports. NULL handling is the caller's job.
func compareValues(a, b any) (int, error) {
	switch av := a.(type) {
	case int32:
		bv, err := toInt64(b)
		if err != nil {
			return 0, err
		}
		return cmpInt64(int64(av), bv), nil
	case int64:
		bv, err := toInt64(b)
		if err != nil {
			return 0, err
		}
		return cmpInt64(av, bv), nil
	case string:
		bs, ok := b.(string)
		if !ok {
			return 0, fmt.Errorf("exec: cannot compare string with %T", b)
		}
		return strings.Compare(av, bs), nil
	case bool:
		bb, ok := b.(bool)
		if !ok {
			return 0, fmt.Errorf("exec: cannot compare bool with %T", b)
		}
		switch {
		case av == bb:
			return 0, nil
		case !av && bb:
			return -1, nil
		default:
			return 1, nil
		}
	case [16]byte:
		bb, ok := b.([16]byte)
		if !ok {
			return 0, fmt.Errorf("exec: cannot compare uuid with %T", b)
		}
		// Lexicographic byte order for UUIDs matches PG's collation.
		for i := 0; i < 16; i++ {
			if av[i] != bb[i] {
				if av[i] < bb[i] {
					return -1, nil
				}
				return 1, nil
			}
		}
		return 0, nil
	case time.Time:
		bt, ok := b.(time.Time)
		if !ok {
			return 0, fmt.Errorf("exec: cannot compare timestamptz with %T", b)
		}
		switch {
		case av.Before(bt):
			return -1, nil
		case av.After(bt):
			return 1, nil
		default:
			return 0, nil
		}
	case []byte:
		bb, ok := b.([]byte)
		if !ok {
			return 0, fmt.Errorf("exec: cannot compare bytea with %T", b)
		}
		return bytes.Compare(av, bb), nil
	default:
		return 0, fmt.Errorf("exec: cannot compare %T", a)
	}
}

func toInt64(v any) (int64, error) {
	switch n := v.(type) {
	case int32:
		return int64(n), nil
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	default:
		return 0, fmt.Errorf("exec: not an integer: %T", v)
	}
}

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
