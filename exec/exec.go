// Package exec walks an ir.Node tree against a storage.Txn and
// produces rows. Operators map almost 1:1 to IR node kinds.
package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
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
	// OuterSchema and OuterRow are set when running a correlated
	// subquery: the inner operator's resolveColumnRef falls back to
	// OuterSchema when a ColumnRef can't be resolved against the
	// inner schema, and per-row evaluation reads outer-scope values
	// from OuterRow.
	OuterSchema []Column
	OuterRow    Row
	// RecursiveFrames is the per-CTE working-set frame stack used
	// while iterating a WITH RECURSIVE plan. Each frame holds the
	// schema and the current set of rows that the step plan should
	// see when it scans the recursive name. Built once by
	// buildRecursive; read by buildRecursiveRef.
	RecursiveFrames map[string]*RecursiveFrame
}

// RecursiveFrame is the working-set view a recursive CTE's step
// plan reads through env.RecursiveFrames. Cols stays fixed; Rows
// changes between iterations to reflect the most recent batch.
type RecursiveFrame struct {
	Cols []Column
	Rows []Row
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
	case *ir.Truncate:
		return buildTruncate(p, env)
	case *ir.CreateView:
		return buildCreateView(p, env), nil
	case *ir.DropView:
		return buildDropView(p, env), nil
	case *ir.AlterTable:
		return buildAlterTable(p, env), nil
	case *ir.CreateIndex:
		return &ddlOp{tag: "CREATE INDEX", do: func() error { return nil }}, nil
	case *ir.DropIndex:
		return &ddlOp{tag: "DROP INDEX", do: func() error { return nil }}, nil
	case *ir.DropTable:
		return buildDropTable(p, env), nil
	case *ir.Insert:
		return buildInsert(p, env)
	case *ir.Delete:
		return buildDelete(p, env)
	case *ir.Update:
		return buildUpdate(p, env)
	case *ir.Aggregate:
		return buildAggregate(p, env)
	case *ir.Distinct:
		return buildDistinct(p, env)
	case *ir.Union:
		return buildUnion(p, env)
	case *ir.SubqueryAlias:
		return buildSubqueryAlias(p, env)
	case *ir.Window:
		return buildWindow(p, env)
	case *ir.Unnest:
		return buildUnnest(p, env)
	case *ir.GenerateSeries:
		return buildGenerateSeries(p, env)
	case *ir.Recursive:
		return buildRecursive(p, env)
	case *ir.RecursiveRef:
		return buildRecursiveRef(p, env)
	default:
		return nil, fmt.Errorf("exec: unsupported plan node %T", plan)
	}
}

// --- Scan ---

func buildScan(p *ir.Scan, env *Env) (Operator, error) {
	if plan, ok := env.Schema.View(p.Table); ok {
		// View references inline the registered plan, optionally
		// re-qualified via the scan's alias.
		alias := p.Alias
		if alias == "" {
			alias = p.Table
		}
		return Build(&ir.SubqueryAlias{Inner: plan, Alias: alias}, env)
	}
	ct, ok := env.Schema.Table(p.Table)
	if !ok {
		return nil, fmt.Errorf("exec: unknown table %q", p.Table)
	}
	st, ok := env.Txn.Table(p.Table)
	if !ok {
		return nil, fmt.Errorf("exec: storage missing table %q", p.Table)
	}
	qualifier := p.Table
	if p.Alias != "" {
		qualifier = p.Alias
	}
	cols := make([]Column, len(ct.Columns))
	for i, c := range ct.Columns {
		cols[i] = Column{Qualifier: qualifier, Name: c.Name, Type: c.Type}
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
	// Expand any StarRef sentinels into one entry per input column
	// before resolving — the rest of the planning pipeline never sees
	// stars.
	expandedExprs, expandedNames := expandStarRefs(p.Exprs, p.OutputNames, inSchema)
	exprs := make([]ir.Expr, len(expandedExprs))
	cols := make([]Column, len(expandedExprs))
	for i, e := range expandedExprs {
		resolved, err := resolveExpr(e, inSchema, env)
		if err != nil {
			return nil, err
		}
		exprs[i] = resolved
		name := ""
		if i < len(expandedNames) {
			name = expandedNames[i]
		}
		cols[i] = Column{Name: name, Type: resolved.Type()}
	}
	return &projectOp{in: in, cols: cols, exprs: exprs, env: env}, nil
}

// expandStarRefs replaces every StarRef in exprs with one ColumnRef
// per column in schema. OutputNames grow in lockstep so each expanded
// column carries the source column's name.
func expandStarRefs(exprs []ir.Expr, names []string, schema []Column) ([]ir.Expr, []string) {
	hasStar := false
	for _, e := range exprs {
		if _, ok := e.(*ir.StarRef); ok {
			hasStar = true
			break
		}
	}
	if !hasStar {
		return exprs, names
	}
	outExprs := make([]ir.Expr, 0, len(exprs)+len(schema))
	outNames := make([]string, 0, len(exprs)+len(schema))
	for i, e := range exprs {
		if star, ok := e.(*ir.StarRef); ok {
			for _, c := range schema {
				if star.Qualifier != "" && star.Qualifier != c.Qualifier {
					continue
				}
				outExprs = append(outExprs, &ir.ColumnRef{Qualifier: c.Qualifier, Name: c.Name})
				outNames = append(outNames, c.Name)
			}
			continue
		}
		outExprs = append(outExprs, e)
		var name string
		if i < len(names) {
			name = names[i]
		}
		outNames = append(outNames, name)
	}
	return outExprs, outNames
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
	leftSchema := left.OutputSchema()
	// Probe the right side once to learn its schema. For a lateral
	// join the probe runs with env.OuterSchema set to left's schema
	// + a zero outer row, so any correlated ColumnRef inside the
	// right plan resolves at probe time without needing a real left
	// row to run.
	rightEnv := *env
	if p.Lateral {
		rightEnv.OuterSchema = leftSchema
		rightEnv.OuterRow = make(Row, len(leftSchema))
	}
	right, err := Build(p.Right, &rightEnv)
	if err != nil {
		left.Close()
		return nil, err
	}
	rightSchema := right.OutputSchema()
	combined := append(append([]Column(nil), leftSchema...), rightSchema...)
	var cond ir.Expr
	if p.Cond != nil {
		cond, err = resolveExpr(p.Cond, combined, env)
		if err != nil {
			left.Close()
			right.Close()
			return nil, err
		}
	}
	op := &joinOp{
		left:     left,
		right:    right,
		cond:     cond,
		cols:     combined,
		env:      env,
		joinType: p.Type,
		rightWid: len(rightSchema),
	}
	if p.Lateral {
		// The probe operator is no longer needed — close it and let
		// joinOp rebuild a fresh right per left row.
		right.Close()
		op.right = nil
		op.lateral = true
		op.rightPlan = p.Right
		op.leftSchema = leftSchema
	}
	return op, nil
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

	// Lateral mode: right plan is rebuilt per left row with the
	// outer scope set, so its expressions can reference left's
	// columns. rightInit / rightRows aren't reused across rows.
	lateral    bool
	rightPlan  ir.Node
	leftSchema []Column

	curLeft    Row
	rightAt    int
	curMatched bool // true if curLeft matched any right row (for LEFT)
}

func (j *joinOp) OutputSchema() []Column { return j.cols }
func (j *joinOp) Close() error {
	lerr := j.left.Close()
	if j.right != nil {
		if rerr := j.right.Close(); rerr != nil && lerr == nil {
			return rerr
		}
	}
	return lerr
}

func (j *joinOp) Next(ctx context.Context) (Row, error) {
	if !j.lateral && !j.rightInit {
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
			if j.lateral {
				if err := j.materialiseLateralRight(); err != nil {
					return nil, err
				}
			}
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

// materialiseLateralRight builds a fresh right operator with the
// outer scope set to the current left row, drains it into rightRows,
// and closes it. Called once per left row so the right plan's
// correlated references see the right outer row.
func (j *joinOp) materialiseLateralRight() error {
	childEnv := *j.env
	childEnv.OuterSchema = j.leftSchema
	childEnv.OuterRow = j.curLeft
	op, err := Build(j.rightPlan, &childEnv)
	if err != nil {
		return fmt.Errorf("LATERAL: %w", err)
	}
	rows, err := drain(op)
	op.Close()
	if err != nil {
		return fmt.Errorf("LATERAL: %w", err)
	}
	j.rightRows = rows
	return nil
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

// --- Distinct ---

// buildDistinct wraps Input in a streaming dedup operator. Equality is
// computed via groupKeyString on each output row's tuple — same hash
// scheme GROUP BY uses, so types we already handle there work here too.
func buildDistinct(p *ir.Distinct, env *Env) (Operator, error) {
	in, err := Build(p.Input, env)
	if err != nil {
		return nil, err
	}
	op := &distinctOp{in: in, seen: map[string]struct{}{}}
	if len(p.On) > 0 {
		op.onKeys = make([]ir.Expr, len(p.On))
		for i, e := range p.On {
			r, err := resolveExpr(e, in.OutputSchema(), env)
			if err != nil {
				in.Close()
				return nil, err
			}
			op.onKeys[i] = r
		}
		op.env = env
	}
	return op, nil
}

type distinctOp struct {
	in     Operator
	seen   map[string]struct{}
	onKeys []ir.Expr // when set, dedupe by these keys instead of full row
	env    *Env
}

func (d *distinctOp) OutputSchema() []Column { return d.in.OutputSchema() }
func (d *distinctOp) Close() error           { return d.in.Close() }

func (d *distinctOp) Next(ctx context.Context) (Row, error) {
	for {
		row, err := d.in.Next(ctx)
		if err != nil {
			return nil, err
		}
		key, err := d.keyFor(row)
		if err != nil {
			return nil, err
		}
		if _, dup := d.seen[key]; dup {
			continue
		}
		d.seen[key] = struct{}{}
		return row, nil
	}
}

func (d *distinctOp) keyFor(row Row) (string, error) {
	if len(d.onKeys) == 0 {
		return groupKeyString([]any(row)), nil
	}
	parts := make([]string, len(d.onKeys))
	for i, e := range d.onKeys {
		v, err := evalExpr(e, row, d.env)
		if err != nil {
			return "", err
		}
		parts[i] = uniqueKey(v)
	}
	return strings.Join(parts, "\x00"), nil
}

// --- Window ---

// buildWindow compiles an ir.Window node. Each WindowCall resolves
// its partition + order keys against the input schema; the operator
// drains the input, sorts rows by (partition, order), and emits each
// row augmented with one extra column per window call.
func buildWindow(p *ir.Window, env *Env) (Operator, error) {
	in, err := Build(p.Input, env)
	if err != nil {
		return nil, err
	}
	inSchema := in.OutputSchema()
	resolved := make([]resolvedWindowCall, len(p.Calls))
	cols := append([]Column(nil), inSchema...)
	for i, c := range p.Calls {
		var err error
		resolved[i].fn = c.Func
		resolved[i].partKeys = make([]ir.Expr, len(c.Spec.PartitionBy))
		for j, e := range c.Spec.PartitionBy {
			resolved[i].partKeys[j], err = resolveExpr(e, inSchema, env)
			if err != nil {
				in.Close()
				return nil, err
			}
		}
		resolved[i].orderKeys = make([]ir.SortKey, len(c.Spec.OrderBy))
		for j, k := range c.Spec.OrderBy {
			r, err := resolveExpr(k.Expr, inSchema, env)
			if err != nil {
				in.Close()
				return nil, err
			}
			resolved[i].orderKeys[j] = ir.SortKey{Expr: r, Desc: k.Desc, Nulls: k.Nulls}
		}
		cols = append(cols, Column{Name: c.Output, Type: types.Int8})
	}
	return &windowOp{in: in, calls: resolved, cols: cols, env: env}, nil
}

type resolvedWindowCall struct {
	fn        string
	partKeys  []ir.Expr
	orderKeys []ir.SortKey
}

type windowOp struct {
	in    Operator
	calls []resolvedWindowCall
	cols  []Column
	env   *Env

	ran     bool
	pending []Row
	pos     int
}

func (w *windowOp) OutputSchema() []Column { return w.cols }
func (w *windowOp) Close() error           { return w.in.Close() }

func (w *windowOp) Next(ctx context.Context) (Row, error) {
	if !w.ran {
		if err := w.run(ctx); err != nil {
			return nil, err
		}
	}
	if w.pos >= len(w.pending) {
		return nil, io.EOF
	}
	r := w.pending[w.pos]
	w.pos++
	return r, nil
}

// run materialises every input row, computes each window function's
// per-partition assignment, and stores the augmented rows in pending
// in their original input order. Because windows can use independent
// PARTITION BY / ORDER BY specs, each call is computed by sorting a
// row-index slice rather than rearranging the underlying data.
func (w *windowOp) run(ctx context.Context) error {
	w.ran = true
	rows, err := drain(w.in)
	if err != nil {
		return err
	}
	// Augment each input row with len(w.calls) extra slots and then
	// fill them per call.
	out := make([]Row, len(rows))
	extraStart := len(w.cols) - len(w.calls)
	for i, r := range rows {
		nr := make(Row, len(w.cols))
		copy(nr, r)
		out[i] = nr
		_ = extraStart
	}
	for ci, c := range w.calls {
		col := extraStart + ci
		assignments, err := computeWindowCall(c, rows, w.env)
		if err != nil {
			return err
		}
		for i, v := range assignments {
			out[i][col] = v
		}
	}
	w.pending = out
	_ = ctx
	return nil
}

// computeWindowCall returns a value per input row index for the
// given window call. row_number / rank / dense_rank ride the same
// sort-then-assign loop; their per-row counter rule differs.
func computeWindowCall(c resolvedWindowCall, rows []Row, env *Env) ([]any, error) {
	n := len(rows)
	out := make([]any, n)
	// Sort row indices by (partition keys, order keys).
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	keys := make([]ir.SortKey, 0, len(c.partKeys)+len(c.orderKeys))
	for _, k := range c.partKeys {
		keys = append(keys, ir.SortKey{Expr: k})
	}
	keys = append(keys, c.orderKeys...)
	var sortErr error
	sort.SliceStable(idx, func(i, j int) bool {
		if sortErr != nil {
			return false
		}
		for _, k := range keys {
			a, err := evalExpr(k.Expr, rows[idx[i]], env)
			if err != nil {
				sortErr = err
				return false
			}
			b, err := evalExpr(k.Expr, rows[idx[j]], env)
			if err != nil {
				sortErr = err
				return false
			}
			if a == nil || b == nil {
				if a == nil && b == nil {
					continue
				}
				return nullSortLess(a == nil, k)
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
	if sortErr != nil {
		return nil, sortErr
	}
	// Walk in sorted order and assign per-call counter values.
	var partKey string
	var rowNum, rank, denseRank int64
	prevOrder := ""
	for pos, ri := range idx {
		curPart, err := keyValuesString(c.partKeys, rows[ri], env)
		if err != nil {
			return nil, err
		}
		if pos == 0 || curPart != partKey {
			partKey = curPart
			rowNum, rank, denseRank = 0, 0, 0
			prevOrder = ""
		}
		rowNum++
		curOrder, err := orderKeysString(c.orderKeys, rows[ri], env)
		if err != nil {
			return nil, err
		}
		if pos == 0 || curOrder != prevOrder {
			rank = rowNum
			denseRank++
			prevOrder = curOrder
		}
		switch c.fn {
		case "row_number":
			out[ri] = rowNum
		case "rank":
			out[ri] = rank
		case "dense_rank":
			out[ri] = denseRank
		default:
			return nil, fmt.Errorf("exec: unsupported window function %q", c.fn)
		}
	}
	return out, nil
}

func keyValuesString(keys []ir.Expr, in Row, env *Env) (string, error) {
	parts := make([]string, len(keys))
	for i, k := range keys {
		v, err := evalExpr(k, in, env)
		if err != nil {
			return "", err
		}
		parts[i] = uniqueKey(v)
	}
	return strings.Join(parts, "\x00"), nil
}

func orderKeysString(keys []ir.SortKey, in Row, env *Env) (string, error) {
	parts := make([]string, len(keys))
	for i, k := range keys {
		v, err := evalExpr(k.Expr, in, env)
		if err != nil {
			return "", err
		}
		parts[i] = uniqueKey(v)
	}
	return strings.Join(parts, "\x00"), nil
}

// --- Unnest ---

// buildUnnest evaluates the array expression once at build time
// (parameters are bound by then) and returns a materialised operator
// that yields one row per element. The output schema has a single
// column named after the alias whose type is the array's element
// type.
func buildUnnest(p *ir.Unnest, env *Env) (Operator, error) {
	resolved, err := resolveExpr(p.Array, nil, env)
	if err != nil {
		return nil, err
	}
	v, err := evalExpr(resolved, nil, env)
	if err != nil {
		return nil, err
	}
	var (
		rows    []Row
		colType types.Type
	)
	switch arr := v.(type) {
	case []int64:
		rows = make([]Row, len(arr))
		for i, n := range arr {
			rows[i] = Row{n}
		}
		colType = types.Int8
	case []int32:
		rows = make([]Row, len(arr))
		for i, n := range arr {
			rows[i] = Row{n}
		}
		colType = types.Int4
	case []string:
		rows = make([]Row, len(arr))
		for i, s := range arr {
			rows[i] = Row{s}
		}
		colType = types.Text
	case nil:
		colType = types.Text
	default:
		return nil, fmt.Errorf("exec: unnest: unsupported array type %T", v)
	}
	cols := []Column{{Qualifier: p.Alias, Name: p.Alias, Type: colType}}
	return &materializedOp{cols: cols, rows: rows}, nil
}

// --- Recursive ---

// buildRecursive iterates a WITH RECURSIVE plan to a fixed point.
// The base materialises into the initial working set; the step
// rebuilds each iteration against the latest working set until it
// stops yielding new rows. UNION (without ALL) deduplicates.
func buildRecursive(p *ir.Recursive, env *Env) (Operator, error) {
	var base, step ir.Node
	if u, ok := p.Plan.(*ir.Union); ok {
		base = u.Left
		step = u.Right
	} else {
		base = p.Plan
	}
	bop, err := Build(base, env)
	if err != nil {
		return nil, err
	}
	cols := append([]Column(nil), bop.OutputSchema()...)
	ctx := context.Background()
	var allRows []Row
	seen := map[string]struct{}{}
	addRow := func(r Row) {
		if !p.UnionAll {
			k := rowKey(r)
			if _, ok := seen[k]; ok {
				return
			}
			seen[k] = struct{}{}
		}
		allRows = append(allRows, r)
	}
	var workingRows []Row
	for {
		r, err := bop.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			bop.Close()
			return nil, err
		}
		before := len(allRows)
		addRow(r)
		if len(allRows) > before {
			workingRows = append(workingRows, r)
		}
	}
	bop.Close()
	if step == nil {
		return &materializedOp{cols: cols, rows: allRows}, nil
	}
	if env.RecursiveFrames == nil {
		env.RecursiveFrames = map[string]*RecursiveFrame{}
	}
	frame := &RecursiveFrame{Cols: cols, Rows: workingRows}
	env.RecursiveFrames[p.Name] = frame
	defer delete(env.RecursiveFrames, p.Name)

	const safetyCap = 100000
	for iter := 0; iter < safetyCap; iter++ {
		if len(frame.Rows) == 0 {
			break
		}
		sop, err := Build(step, env)
		if err != nil {
			return nil, err
		}
		var nextRows []Row
		for {
			r, err := sop.Next(ctx)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				sop.Close()
				return nil, err
			}
			before := len(allRows)
			addRow(r)
			if len(allRows) > before {
				nextRows = append(nextRows, r)
			}
		}
		sop.Close()
		if len(nextRows) == 0 {
			break
		}
		frame.Rows = nextRows
	}
	return &materializedOp{cols: cols, rows: allRows}, nil
}

func buildRecursiveRef(p *ir.RecursiveRef, env *Env) (Operator, error) {
	frame, ok := env.RecursiveFrames[p.Name]
	if !ok {
		return nil, fmt.Errorf("exec: recursive reference %q not in scope", p.Name)
	}
	rows := make([]Row, len(frame.Rows))
	for i, r := range frame.Rows {
		rows[i] = append(Row(nil), r...)
	}
	return &materializedOp{cols: frame.Cols, rows: rows}, nil
}

// rowKey is a coarse string key for dedup of recursive UNION rows.
// fmt.Sprint is good enough for the small sets recursive CTEs
// typically produce.
func rowKey(r Row) string {
	return fmt.Sprintf("%v", []any(r))
}

// --- GenerateSeries ---

// buildGenerateSeries materialises rows for `generate_series(start,
// stop[, step])`. All three args are evaluated once at build time
// (parameters are bound by then). The output column type matches
// the widest argument type — int8 if any arg is int8, else int4.
// Step defaults to 1 when omitted; a zero step is rejected (PG
// raises 22023). Empty results when the range can't progress.
func buildGenerateSeries(p *ir.GenerateSeries, env *Env) (Operator, error) {
	start, err := evalIntArg(p.Start, env)
	if err != nil {
		return nil, fmt.Errorf("generate_series: start: %w", err)
	}
	stop, err := evalIntArg(p.Stop, env)
	if err != nil {
		return nil, fmt.Errorf("generate_series: stop: %w", err)
	}
	step := int64(1)
	stepWide := false
	if p.Step != nil {
		s, err := evalIntArg(p.Step, env)
		if err != nil {
			return nil, fmt.Errorf("generate_series: step: %w", err)
		}
		step = s.v
		stepWide = s.wide
		if step == 0 {
			return nil, &SQLError{Code: "22023", Message: "step size cannot equal zero"}
		}
	}
	wide := start.wide || stop.wide || stepWide
	colType := types.Int4
	if wide {
		colType = types.Int8
	}
	var rows []Row
	if step > 0 {
		for v := start.v; v <= stop.v; v += step {
			rows = append(rows, gsRow(v, wide))
		}
	} else {
		for v := start.v; v >= stop.v; v += step {
			rows = append(rows, gsRow(v, wide))
		}
	}
	cols := []Column{{Qualifier: p.Alias, Name: "generate_series", Type: colType}}
	return &materializedOp{cols: cols, rows: rows}, nil
}

type intArg struct {
	v    int64
	wide bool // true when the source was int64 / int8
}

func evalIntArg(e ir.Expr, env *Env) (intArg, error) {
	resolved, err := resolveExpr(e, nil, env)
	if err != nil {
		return intArg{}, err
	}
	v, err := evalExpr(resolved, nil, env)
	if err != nil {
		return intArg{}, err
	}
	switch x := v.(type) {
	case int32:
		return intArg{v: int64(x), wide: false}, nil
	case int64:
		return intArg{v: x, wide: true}, nil
	default:
		return intArg{}, fmt.Errorf("expected integer, got %T", v)
	}
}

func gsRow(v int64, wide bool) Row {
	if wide {
		return Row{v}
	}
	return Row{int32(v)}
}

// --- SubqueryAlias ---

func buildSubqueryAlias(p *ir.SubqueryAlias, env *Env) (Operator, error) {
	in, err := Build(p.Inner, env)
	if err != nil {
		return nil, err
	}
	cols := append([]Column(nil), in.OutputSchema()...)
	for i := range cols {
		cols[i].Qualifier = p.Alias
	}
	return &subqueryAliasOp{in: in, cols: cols}, nil
}

type subqueryAliasOp struct {
	in   Operator
	cols []Column
}

func (s *subqueryAliasOp) OutputSchema() []Column              { return s.cols }
func (s *subqueryAliasOp) Close() error                        { return s.in.Close() }
func (s *subqueryAliasOp) Next(c context.Context) (Row, error) { return s.in.Next(c) }

// --- Union ---

// buildUnion compiles `Left UNION [ALL] Right`. Output schema is taken
// from Left; if the two sides disagree on column count we error at
// build time. UNION (without ALL) wraps the result in a distinct op.
func buildUnion(p *ir.Union, env *Env) (Operator, error) {
	left, err := Build(p.Left, env)
	if err != nil {
		return nil, err
	}
	right, err := Build(p.Right, env)
	if err != nil {
		left.Close()
		return nil, err
	}
	if len(left.OutputSchema()) != len(right.OutputSchema()) {
		left.Close()
		right.Close()
		return nil, fmt.Errorf("exec: UNION column count mismatch: %d vs %d",
			len(left.OutputSchema()), len(right.OutputSchema()))
	}
	var out Operator = &unionOp{left: left, right: right, cols: left.OutputSchema()}
	if !p.All {
		out = &distinctOp{in: out, seen: map[string]struct{}{}}
	}
	return out, nil
}

type unionOp struct {
	left      Operator
	right     Operator
	cols      []Column
	leftDone  bool
	rightDone bool
}

func (u *unionOp) OutputSchema() []Column { return u.cols }
func (u *unionOp) Close() error {
	lerr := u.left.Close()
	rerr := u.right.Close()
	if lerr != nil {
		return lerr
	}
	return rerr
}

func (u *unionOp) Next(ctx context.Context) (Row, error) {
	if !u.leftDone {
		row, err := u.left.Next(ctx)
		if errors.Is(err, io.EOF) {
			u.leftDone = true
		} else {
			return row, err
		}
	}
	if !u.rightDone {
		row, err := u.right.Next(ctx)
		if errors.Is(err, io.EOF) {
			u.rightDone = true
			return nil, io.EOF
		}
		return row, err
	}
	return nil, io.EOF
}

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
		keys[i] = ir.SortKey{Expr: resolved, Desc: k.Desc, Nulls: k.Nulls}
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

// nullSortLess decides whether the i-th row should sort before the
// j-th row when one operand of the current sort key is NULL. aIsNull
// is true iff the i-th row's key value is NULL (the j-th is non-NULL,
// since the all-NULL case is handled by the caller).
//
// PG default: ASC sorts NULLs LAST, DESC sorts NULLs FIRST. Explicit
// NULLS FIRST/LAST overrides the default for the key.
func nullSortLess(aIsNull bool, k ir.SortKey) bool {
	nullsFirst := k.Desc
	switch k.Nulls {
	case ir.NullsFirst:
		nullsFirst = true
	case ir.NullsLast:
		nullsFirst = false
	}
	if aIsNull {
		return nullsFirst
	}
	return !nullsFirst
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
			if a == nil || b == nil {
				if a == nil && b == nil {
					continue
				}
				return nullSortLess(a == nil, k)
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
			cols[i] = catalog.Column{Name: c.Name, Type: c.Type, NotNull: c.NotNull, Unique: c.Unique, Auto: c.Auto, Default: c.Default}
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
		for _, fk := range p.TableFKs {
			idx := -1
			for i, c := range cols {
				if c.Name == fk.Column {
					idx = i
					break
				}
			}
			if idx < 0 {
				return &SQLError{Code: "42703", Message: fmt.Sprintf("column %q named in FOREIGN KEY does not exist", fk.Column)}
			}
			if cols[idx].References.Table != "" {
				return &SQLError{Code: "42710", Message: fmt.Sprintf("column %q already has a FOREIGN KEY", fk.Column)}
			}
			cols[idx].References = catalog.ColumnRef{
				Table:    fk.Ref.Table,
				Column:   fk.Ref.Column,
				OnDelete: catalog.OnDeleteAction(fk.Ref.OnDelete),
			}
		}
		for _, tc := range p.TableChecks {
			n := tc.Name
			if n == "" {
				n = p.Name + "_check"
			}
			checks = append(checks, catalog.Check{Name: n, Expr: tc.Expr})
		}
		if err := env.Schema.CreateTable(catalog.Table{Name: p.Name, Columns: cols, Checks: checks}); err != nil {
			return err
		}
		env.Engine.CreateTable(p.Name, len(cols))
		return nil
	}}
}

// buildAlterTable mutates the catalog and reshapes storage rows for
// ADD/DROP/RENAME COLUMN. Rows are rewritten in place: ADD appends
// a NULL slot, DROP removes the corresponding slot, RENAME leaves
// row data untouched (only the catalog column name changes).
func buildAlterTable(p *ir.AlterTable, env *Env) Operator {
	return &ddlOp{tag: "ALTER TABLE", do: func() error {
		tbl, ok := env.Schema.Table(p.Table)
		if !ok {
			return &SQLError{Code: "42P01", Message: fmt.Sprintf("table %q does not exist", p.Table)}
		}
		switch p.Action {
		case ir.AlterTableAddColumn:
			return alterTableAdd(env, tbl, p.AddCol)
		case ir.AlterTableDropColumn:
			return alterTableDrop(env, tbl, p.DropName, p.IfExistsCol)
		case ir.AlterTableRenameColumn:
			return alterTableRename(env, tbl, p.RenameOld, p.RenameNew)
		case ir.AlterTableSetNotNull:
			return alterTableSetNotNull(env, tbl, p.AlterCol, true)
		case ir.AlterTableDropNotNull:
			return alterTableSetNotNull(env, tbl, p.AlterCol, false)
		default:
			return fmt.Errorf("exec: unknown ALTER TABLE action %d", p.Action)
		}
	}}
}

func alterTableAdd(env *Env, tbl catalog.Table, def ir.ColumnDef) error {
	for _, c := range tbl.Columns {
		if c.Name == def.Name {
			return &SQLError{Code: "42701", Message: fmt.Sprintf("column %q of relation %q already exists", def.Name, tbl.Name)}
		}
	}
	st, ok := env.Txn.Table(tbl.Name)
	if !ok {
		return fmt.Errorf("exec: storage missing table %q", tbl.Name)
	}
	rows := st.Rows()
	if def.NotNull && len(rows) > 0 {
		return &SQLError{Code: "23502", Message: fmt.Sprintf("column %q contains null values", def.Name)}
	}
	newCol := catalog.Column{Name: def.Name, Type: def.Type, NotNull: def.NotNull, Unique: def.Unique, Auto: def.Auto, Default: def.Default}
	if def.References != nil {
		newCol.References = catalog.ColumnRef{
			Table:    def.References.Table,
			Column:   def.References.Column,
			OnDelete: catalog.OnDeleteAction(def.References.OnDelete),
		}
	}
	tbl.Columns = append(tbl.Columns, newCol)
	if def.Check != nil {
		tbl.Checks = append(tbl.Checks, catalog.Check{
			Name: tbl.Name + "_" + def.Name + "_check",
			Expr: def.Check,
		})
	}
	if err := env.Schema.CreateTable(tbl); err != nil {
		return err
	}
	st.Mutate(func(rs []storage.Row) []storage.Row {
		for i := range rs {
			rs[i] = append(rs[i], nil)
		}
		return rs
	})
	return nil
}

func alterTableDrop(env *Env, tbl catalog.Table, name string, ifExists bool) error {
	idx := -1
	for i, c := range tbl.Columns {
		if c.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		if ifExists {
			return nil
		}
		return &SQLError{Code: "42703", Message: fmt.Sprintf("column %q of relation %q does not exist", name, tbl.Name)}
	}
	st, ok := env.Txn.Table(tbl.Name)
	if !ok {
		return fmt.Errorf("exec: storage missing table %q", tbl.Name)
	}
	tbl.Columns = append(tbl.Columns[:idx:idx], tbl.Columns[idx+1:]...)
	if err := env.Schema.CreateTable(tbl); err != nil {
		return err
	}
	st.Mutate(func(rs []storage.Row) []storage.Row {
		for i := range rs {
			r := rs[i]
			if idx >= len(r) {
				continue
			}
			rs[i] = append(r[:idx:idx], r[idx+1:]...)
		}
		return rs
	})
	return nil
}

func alterTableSetNotNull(env *Env, tbl catalog.Table, name string, notNull bool) error {
	idx := -1
	for i, c := range tbl.Columns {
		if c.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return &SQLError{Code: "42703", Message: fmt.Sprintf("column %q of relation %q does not exist", name, tbl.Name)}
	}
	if notNull {
		st, ok := env.Txn.Table(tbl.Name)
		if !ok {
			return fmt.Errorf("exec: storage missing table %q", tbl.Name)
		}
		for _, r := range st.Rows() {
			if idx < len(r) && r[idx] == nil {
				return &SQLError{Code: "23502", Message: fmt.Sprintf("column %q contains null values", name)}
			}
		}
	}
	cols := append([]catalog.Column(nil), tbl.Columns...)
	cols[idx].NotNull = notNull
	tbl.Columns = cols
	return env.Schema.CreateTable(tbl)
}

func alterTableRename(env *Env, tbl catalog.Table, oldName, newName string) error {
	idx := -1
	for i, c := range tbl.Columns {
		if c.Name == oldName {
			idx = i
		}
		if c.Name == newName {
			return &SQLError{Code: "42701", Message: fmt.Sprintf("column %q of relation %q already exists", newName, tbl.Name)}
		}
	}
	if idx < 0 {
		return &SQLError{Code: "42703", Message: fmt.Sprintf("column %q of relation %q does not exist", oldName, tbl.Name)}
	}
	cols := append([]catalog.Column(nil), tbl.Columns...)
	cols[idx].Name = newName
	tbl.Columns = cols
	return env.Schema.CreateTable(tbl)
}

func buildTruncate(p *ir.Truncate, env *Env) (Operator, error) {
	for _, name := range p.Tables {
		if _, ok := env.Schema.Table(name); !ok {
			return nil, &SQLError{Code: "42P01", Message: fmt.Sprintf("table %q does not exist", name)}
		}
	}
	return &ddlOp{tag: "TRUNCATE TABLE", do: func() error {
		for _, name := range p.Tables {
			st, ok := env.Txn.Table(name)
			if !ok {
				return fmt.Errorf("exec: storage missing table %q", name)
			}
			st.Mutate(func(_ []storage.Row) []storage.Row { return nil })
		}
		return nil
	}}, nil
}

func buildCreateView(p *ir.CreateView, env *Env) Operator {
	return &ddlOp{tag: "CREATE VIEW", do: func() error {
		if _, ok := env.Schema.Table(p.Name); ok {
			return &SQLError{Code: "42P07", Message: fmt.Sprintf("relation %q already exists", p.Name)}
		}
		return env.Schema.CreateView(p.Name, p.Plan)
	}}
}

func buildDropView(p *ir.DropView, env *Env) Operator {
	return &ddlOp{tag: "DROP VIEW", do: func() error {
		if !env.Schema.DropView(p.Name) && !p.IfExists {
			return &SQLError{Code: "42P01", Message: fmt.Sprintf("view %q does not exist", p.Name)}
		}
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
	var (
		colMap       []int
		resolvedRows [][]ir.Expr
	)
	if p.DefaultValues {
		// Single all-default row: empty colMap (no user-supplied
		// values) and one empty row tuple. Auto columns get filled
		// in by the per-row pipeline; everything else stays NULL,
		// which the existing NOT NULL check catches if applicable.
		colMap = nil
		resolvedRows = [][]ir.Expr{nil}
	} else {
		var err error
		colMap, err = buildInsertColumnMap(ct, p.Columns)
		if err != nil {
			return nil, err
		}
		// Resolve each row's expressions against an empty input
		// schema — VALUES expressions don't see column refs.
		resolvedRows = make([][]ir.Expr, len(p.Rows))
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
	}
	op := &insertOp{
		table:      st,
		ct:         ct,
		colMap:     colMap,
		rows:       resolvedRows,
		env:        env,
		onConflict: p.OnConflict,
	}
	if p.Source != nil {
		if len(p.Rows) > 0 {
			return nil, fmt.Errorf("exec: insert with both VALUES and SELECT source")
		}
		src, err := Build(p.Source, env)
		if err != nil {
			return nil, err
		}
		if got, want := len(src.OutputSchema()), len(colMap); got != want {
			src.Close()
			return nil, fmt.Errorf("exec: INSERT ... SELECT column count mismatch: got %d, want %d", got, want)
		}
		op.source = src
	}
	if p.OnConflict != nil {
		idxs, err := resolveConflictColumns(ct, p.OnConflict.Columns)
		if err != nil {
			return nil, err
		}
		op.conflictColIdx = idxs
		updates, err := buildConflictUpdates(ct, p.OnConflict, env)
		if err != nil {
			return nil, err
		}
		op.updateExprs = updates
	}
	if len(p.Returning) > 0 {
		// RETURNING expressions see the post-INSERT row, so their column
		// refs resolve against the table's full schema (catalog order),
		// not the INSERT's column list.
		tableSchema := tableSchemaCols(ct)
		exprs, cols, err := resolveReturning(p.Returning, p.ReturningNames, tableSchema, env)
		if err != nil {
			return nil, err
		}
		op.returning = exprs
		op.returningCols = cols
	}
	return op, nil
}

// tableSchemaCols turns the catalog row into the per-column metadata
// the resolver needs.
func tableSchemaCols(ct catalog.Table) []Column {
	out := make([]Column, len(ct.Columns))
	for i, c := range ct.Columns {
		out[i] = Column{Name: c.Name, Type: c.Type}
	}
	return out
}

// resolveReturning expands any StarRef in the RETURNING list to one
// ColumnRef per schema column, then resolves each entry against the
// table schema. Returns parallel slices ready to drop into an op's
// returning / returningCols fields.
func resolveReturning(exprs []ir.Expr, names []string, schema []Column, env *Env) ([]ir.Expr, []Column, error) {
	expExprs, expNames := expandStarRefs(exprs, names, schema)
	resolved := make([]ir.Expr, len(expExprs))
	cols := make([]Column, len(expExprs))
	for k, e := range expExprs {
		r, err := resolveExpr(e, schema, env)
		if err != nil {
			return nil, nil, err
		}
		resolved[k] = r
		var name string
		if k < len(expNames) {
			name = expNames[k]
		}
		cols[k] = Column{Name: name, Type: r.Type()}
	}
	return resolved, cols, nil
}

// conflictUpdate is a resolved DO UPDATE SET assignment.
type conflictUpdate struct {
	colIdx int     // catalog column index this assignment targets
	expr   ir.Expr // resolved against [existing ++ excluded] schema
}

// buildConflictUpdates resolves DO UPDATE SET assignments against a
// schema that exposes both the existing row (qualifier = table name)
// and the proposed row (qualifier = "excluded"), so an expression can
// reference either side and the resolver disambiguates by qualifier.
func buildConflictUpdates(ct catalog.Table, oc *ir.OnConflict, env *Env) ([]conflictUpdate, error) {
	if len(oc.DoUpdate) == 0 {
		return nil, nil
	}
	// Existing-row columns get an empty qualifier so bare `name`
	// resolves to them unambiguously. Excluded-row columns carry the
	// "excluded" qualifier so `excluded.name` finds them. Real PG
	// also accepts the table name as an explicit qualifier for the
	// existing side, but we don't yet — bare names are enough for
	// the typical sqlc upsert.
	merged := make([]Column, 0, len(ct.Columns)*2)
	for _, c := range ct.Columns {
		merged = append(merged, Column{Name: c.Name, Type: c.Type})
	}
	for _, c := range ct.Columns {
		merged = append(merged, Column{Qualifier: "excluded", Name: c.Name, Type: c.Type})
	}
	out := make([]conflictUpdate, len(oc.DoUpdate))
	for k, a := range oc.DoUpdate {
		idx := -1
		for j, c := range ct.Columns {
			if c.Name == a.Column {
				idx = j
				break
			}
		}
		if idx < 0 {
			return nil, fmt.Errorf("exec: ON CONFLICT DO UPDATE: unknown column %q", a.Column)
		}
		r, err := resolveExpr(a.Expr, merged, env)
		if err != nil {
			return nil, err
		}
		out[k] = conflictUpdate{colIdx: idx, expr: r}
	}
	return out, nil
}

// resolveConflictColumns maps each ON CONFLICT target column name to
// its index in the catalog. Errors if any name is unknown.
func resolveConflictColumns(ct catalog.Table, cols []string) ([]int, error) {
	out := make([]int, len(cols))
	for k, name := range cols {
		idx := -1
		for j, c := range ct.Columns {
			if c.Name == name {
				idx = j
				break
			}
		}
		if idx < 0 {
			return nil, fmt.Errorf("exec: ON CONFLICT: unknown column %q", name)
		}
		out[k] = idx
	}
	return out, nil
}

// filterConflicts drops every built row that already has a matching
// row in `existing` on the conflict-target columns (DO NOTHING).
// Equality uses compareValues so we ride the same coercion table as
// `=`. Rows whose conflict column is NULL are kept — NULL ≠ NULL
// matches PG's IS-DISTINCT-FROM treatment for unique constraints.
func filterConflicts(built []storage.Row, existing []storage.Row, idxs []int) []storage.Row {
	out := make([]storage.Row, 0, len(built))
	for _, row := range built {
		if rowConflicts(row, existing, idxs) {
			continue
		}
		out = append(out, row)
	}
	return out
}

func rowConflicts(row storage.Row, existing []storage.Row, idxs []int) bool {
	for _, ex := range existing {
		match := true
		for _, idx := range idxs {
			a, b := row[idx], ex[idx]
			if a == nil || b == nil {
				match = false
				break
			}
			cmp, err := compareValues(a, b)
			if err != nil || cmp != 0 {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
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
	table          storage.Table
	ct             catalog.Table
	colMap         []int
	rows           [][]ir.Expr
	source         Operator // INSERT ... SELECT — drained at runOnce
	env            *Env
	onConflict     *ir.OnConflict
	conflictColIdx []int
	// updateExprs is parallel to onConflict.DoUpdate: each entry's
	// expression is resolved against the [existing ++ excluded]
	// schema. The Column field carries the catalog index of the
	// target column.
	updateExprs []conflictUpdate
	done        bool
	inserted    int

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
	defaultCols, err := resolveDefaultColumns(i.ct, i.colMap, i.env)
	if err != nil {
		return err
	}
	rawRows, err := i.collectInputRows()
	if err != nil {
		return err
	}
	built := make([]storage.Row, len(rawRows))
	for r, vals := range rawRows {
		row := make(storage.Row, len(i.ct.Columns))
		for j, v := range vals {
			row[i.colMap[j]] = v
		}
		fillAutoColumns(row, i.ct, autoCols, i.table)
		if err := fillDefaultColumns(row, defaultCols, i.env); err != nil {
			return err
		}
		if err := checkNotNull(i.ct, row); err != nil {
			return err
		}
		built[r] = row
	}
	if i.onConflict != nil && i.onConflict.DoNothing {
		built = filterConflicts(built, i.table.Rows(), i.conflictColIdx)
	}
	var updated []storage.Row
	if i.onConflict != nil && len(i.updateExprs) > 0 {
		var err error
		built, updated, err = i.applyDoUpdate(built)
		if err != nil {
			return err
		}
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
		all := append(append([]storage.Row(nil), built...), updated...)
		i.pending = make([]Row, len(all))
		for k, row := range all {
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

// collectInputRows materialises the raw tuples this INSERT will write.
// VALUES branches evaluate each expression in advance; INSERT ...
// SELECT drains the source operator and treats each output row as
// the next tuple.
func (i *insertOp) collectInputRows() ([][]any, error) {
	if i.source != nil {
		defer i.source.Close()
		var out [][]any
		for {
			r, err := i.source.Next(context.Background())
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			if err != nil {
				return nil, err
			}
			vals := make([]any, len(r))
			copy(vals, []any(r))
			out = append(out, vals)
		}
	}
	out := make([][]any, len(i.rows))
	for r, exprRow := range i.rows {
		vals := make([]any, len(exprRow))
		for j, e := range exprRow {
			v, err := evalExpr(e, nil, i.env)
			if err != nil {
				return nil, err
			}
			vals[j] = v
		}
		out[r] = vals
	}
	return out, nil
}

// applyDoUpdate splits `built` into rows that don't conflict (returned
// as the new built list) and rows that do (rewritten in storage). The
// second return is the list of post-update rows, used so RETURNING can
// emit a row per UPDATE just like real PG.
func (i *insertOp) applyDoUpdate(built []storage.Row) (kept, updated []storage.Row, err error) {
	existing := i.table.Rows()
	kept = built[:0]
	for _, proposed := range built {
		idx := findConflictRow(existing, proposed, i.conflictColIdx)
		if idx < 0 {
			kept = append(kept, proposed)
			continue
		}
		merged := append(append(Row(nil), Row(existing[idx])...), Row(proposed)...)
		newRow := append(storage.Row(nil), existing[idx]...)
		for _, u := range i.updateExprs {
			v, evalErr := evalExpr(u.expr, merged, i.env)
			if evalErr != nil {
				return nil, nil, evalErr
			}
			newRow[u.colIdx] = v
		}
		if err := checkNotNull(i.ct, newRow); err != nil {
			return nil, nil, err
		}
		i.table.Mutate(func(rows []storage.Row) []storage.Row {
			for j := range rows {
				if rowsEqual(rows[j], existing[idx]) {
					rows[j] = newRow
					break
				}
			}
			return rows
		})
		// Refresh existing snapshot so the next conflict check sees the
		// updated row — relevant when the update changes a conflict-target
		// column.
		existing = i.table.Rows()
		updated = append(updated, newRow)
		i.inserted++
	}
	return kept, updated, nil
}

// findConflictRow returns the index of the first row in `existing`
// that matches `row` on every conflict-target column, or -1 if none.
func findConflictRow(existing []storage.Row, row storage.Row, idxs []int) int {
	for j, ex := range existing {
		match := true
		for _, idx := range idxs {
			a, b := row[idx], ex[idx]
			if a == nil || b == nil {
				match = false
				break
			}
			cmp, err := compareValues(a, b)
			if err != nil || cmp != 0 {
				match = false
				break
			}
		}
		if match {
			return j
		}
	}
	return -1
}

// rowsEqual is a simple identity test used by Mutate's update closure
// to find the slot for the row we're rewriting. Pointer equality on
// the underlying slice would be cleaner but storage.Row is a slice,
// not a pointer, so we compare element by element.
func rowsEqual(a, b storage.Row) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] == nil && b[i] == nil {
			continue
		}
		if a[i] == nil || b[i] == nil {
			return false
		}
		cmp, err := compareValues(a[i], b[i])
		if err != nil || cmp != 0 {
			return false
		}
	}
	return true
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

// defaultColumn pairs a catalog column index with its resolved
// DEFAULT expression. Used by insertOp to fill columns the INSERT
// didn't mention (and that aren't Auto).
type defaultColumn struct {
	idx  int
	expr ir.Expr
}

// resolveDefaultColumns returns one entry per catalog column that has
// a DEFAULT and was not in colMap (and is not Auto, since Auto wins).
// The expression is resolved once against an empty schema — DEFAULTs
// can't reference other columns of the same row in pgmem-go today.
func resolveDefaultColumns(ct catalog.Table, colMap []int, env *Env) ([]defaultColumn, error) {
	mentioned := make(map[int]bool, len(colMap))
	for _, idx := range colMap {
		mentioned[idx] = true
	}
	var out []defaultColumn
	for idx, c := range ct.Columns {
		if c.Default == nil || mentioned[idx] || c.Auto {
			continue
		}
		expr, err := resolveExpr(c.Default, nil, env)
		if err != nil {
			return nil, err
		}
		out = append(out, defaultColumn{idx: idx, expr: expr})
	}
	return out, nil
}

// fillDefaultColumns evaluates each resolved default and writes the
// result into the row's slot. Skipped if the slot already holds a
// non-nil value (Auto column path may have populated it).
func fillDefaultColumns(row storage.Row, defaults []defaultColumn, env *Env) error {
	for _, d := range defaults {
		if d.idx < len(row) && row[d.idx] != nil {
			continue
		}
		v, err := evalExpr(d.expr, nil, env)
		if err != nil {
			return err
		}
		row[d.idx] = v
	}
	return nil
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
		tableSchema[i] = Column{Qualifier: ct.Name, Name: c.Name, Type: c.Type}
	}
	op := &deleteOp{table: st, ct: ct, env: env, tableSchema: tableSchema}
	resolveSchema := tableSchema
	if p.Using != nil {
		usingOp, err := Build(p.Using, env)
		if err != nil {
			return nil, err
		}
		op.usingCols = append([]Column(nil), usingOp.OutputSchema()...)
		op.usingRows, err = drain(usingOp)
		usingOp.Close()
		if err != nil {
			return nil, err
		}
		resolveSchema = append(append([]Column(nil), tableSchema...), op.usingCols...)
	}
	if p.Where != nil {
		cond, err := resolveExpr(p.Where, resolveSchema, env)
		if err != nil {
			return nil, err
		}
		op.where = cond
	}
	if len(p.Returning) > 0 {
		exprs, cols, err := resolveReturning(p.Returning, p.ReturningNames, tableSchema, env)
		if err != nil {
			return nil, err
		}
		op.returning = exprs
		op.returningCols = cols
	}
	return op, nil
}

type deleteOp struct {
	table       storage.Table
	ct          catalog.Table
	tableSchema []Column
	where       ir.Expr // nil → delete all
	env         *Env

	// Optional USING scope. usingRows is the materialised auxiliary
	// rows; usingCols is its schema. WHERE sees both target and
	// using-side columns when usingRows is set.
	usingRows []Row
	usingCols []Column

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
	if len(d.usingRows) == 0 {
		v, err := evalExpr(d.where, Row(row), d.env)
		if err != nil {
			return false, err
		}
		b, ok := v.(bool)
		return ok && b, nil
	}
	for _, ur := range d.usingRows {
		combined := concatRows(Row(row), ur)
		v, err := evalExpr(d.where, combined, d.env)
		if err != nil {
			return false, err
		}
		if b, ok := v.(bool); ok && b {
			return true, nil
		}
	}
	return false, nil
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
		tableSchema[i] = Column{Qualifier: ct.Name, Name: c.Name, Type: c.Type}
	}
	op := &updateOp{table: st, ct: ct, tableSchema: tableSchema, env: env}

	// Resolution schema: target schema, optionally extended with the
	// FROM tree's columns. assignments / WHERE / RETURNING all see
	// this combined view so SET expressions and predicates can
	// reference auxiliary tables.
	resolveSchema := tableSchema
	var fromCols []Column
	var fromRows []Row
	if p.From != nil {
		fromOp, err := Build(p.From, env)
		if err != nil {
			return nil, err
		}
		fromCols = append([]Column(nil), fromOp.OutputSchema()...)
		fromRows, err = drain(fromOp)
		fromOp.Close()
		if err != nil {
			return nil, err
		}
		resolveSchema = append(append([]Column(nil), tableSchema...), fromCols...)
		op.fromCols = fromCols
		op.fromRows = fromRows
		op.targetWidth = len(tableSchema)
	}

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
		expr, err := resolveExpr(a.Expr, resolveSchema, env)
		if err != nil {
			return nil, err
		}
		op.assigns[i] = resolvedAssign{colIdx: colIdx, expr: expr}
	}

	if p.Where != nil {
		cond, err := resolveExpr(p.Where, resolveSchema, env)
		if err != nil {
			return nil, err
		}
		op.where = cond
	}

	if len(p.Returning) > 0 {
		// RETURNING runs against the post-update target row only —
		// from rows aren't visible.
		exprs, cols, err := resolveReturning(p.Returning, p.ReturningNames, tableSchema, env)
		if err != nil {
			return nil, err
		}
		op.returning = exprs
		op.returningCols = cols
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

	// Optional FROM scope. fromRows is the materialised auxiliary
	// rows; fromCols is its schema. targetWidth is len(tableSchema)
	// — the offset where from-side columns start in a combined row.
	fromRows    []Row
	fromCols    []Column
	targetWidth int

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
			combined, ok, err := u.firstMatchingCombined(row)
			if err != nil {
				evalErr = err
				return rows
			}
			if !ok {
				next[i] = row
				continue
			}
			updated, err := u.applyAssignmentsCombined(row, combined)
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

// firstMatchingCombined returns the (target ++ from) row that
// matches WHERE for the given target. With no FROM scope it just
// evaluates WHERE against the target row. With FROM, it iterates
// the materialised from rows and stops at the first combo whose
// WHERE evaluates true. Returns (combined, false, nil) when no
// match is found.
func (u *updateOp) firstMatchingCombined(row storage.Row) (Row, bool, error) {
	if len(u.fromRows) == 0 {
		ok, err := u.evalWhere(Row(row))
		if err != nil {
			return nil, false, err
		}
		return Row(row), ok, nil
	}
	for _, fr := range u.fromRows {
		combined := concatRows(Row(row), fr)
		ok, err := u.evalWhere(combined)
		if err != nil {
			return nil, false, err
		}
		if ok {
			return combined, true, nil
		}
	}
	return nil, false, nil
}

func (u *updateOp) evalWhere(row Row) (bool, error) {
	if u.where == nil {
		return true, nil
	}
	v, err := evalExpr(u.where, row, u.env)
	if err != nil {
		return false, err
	}
	b, ok := v.(bool)
	return ok && b, nil
}

// applyAssignmentsCombined returns a new target row with each
// assignment evaluated against the combined (target ++ from) row.
// PG semantics: assignments don't see each other's effects within
// the same UPDATE.
func (u *updateOp) applyAssignmentsCombined(orig storage.Row, combined Row) (storage.Row, error) {
	out := append(storage.Row(nil), orig...)
	for _, a := range u.assigns {
		v, err := evalExpr(a.expr, combined, u.env)
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
		// A bare reference can be disambiguated when exactly one match
		// has an empty qualifier — that's how DO UPDATE SET resolves
		// `name` (existing row) vs `excluded.name` (proposed row).
		if ref.Qualifier == "" {
			unq := -1
			for _, m := range matches {
				if schema[m].Qualifier == "" {
					if unq != -1 {
						unq = -1
						break
					}
					unq = m
				}
			}
			if unq >= 0 {
				return unq, nil
			}
		}
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
		// Already-resolved Outer refs pass through unchanged — they
		// were tagged when the surrounding subquery was resolved
		// against its outer scope.
		if x.Outer {
			return x, nil
		}
		idx, err := resolveColumnRef(x, schema)
		if err == nil {
			return &ir.ColumnRef{Qualifier: x.Qualifier, Name: x.Name, Index: idx, T: schema[idx].Type}, nil
		}
		// Fall through to outer scope when the inner schema can't
		// resolve the ref; that's how correlated subqueries find
		// `r.id` referenced inside an EXISTS body.
		if env != nil && len(env.OuterSchema) > 0 {
			if oidx, oerr := resolveColumnRef(x, env.OuterSchema); oerr == nil {
				return &ir.ColumnRef{
					Qualifier: x.Qualifier,
					Name:      x.Name,
					Index:     oidx,
					T:         env.OuterSchema[oidx].Type,
					Outer:     true,
				}, nil
			}
		}
		return nil, err
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
			t = binOpResultType(x.Op, l.Type(), r.Type())
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
		return resolveScalarSubquery(x, schema, env)
	case *ir.InListExpr:
		return resolveInList(x, schema, env)
	case *ir.InSubqueryExpr:
		return resolveInSubquery(x, schema, env)
	case *ir.Cast:
		return resolveCast(x, schema, env)
	case *ir.Case:
		return resolveCase(x, schema, env)
	case *ir.ExistsExpr:
		return resolveExists(x, schema, env)
	case *ir.AnyExpr:
		return resolveAny(x, schema, env)
	default:
		return nil, fmt.Errorf("exec: unsupported expr %T", e)
	}
}

// resolveExists captures the outer schema on the ExistsExpr so the
// per-row evaluator can stamp it on env.OuterSchema for the inner
// Build. Pre-evaluation only happens when the inner plan has no
// outer-scope references — that's the uncorrelated fast path the
// previous evalExists used.
func resolveExists(x *ir.ExistsExpr, schema []Column, env *Env) (ir.Expr, error) {
	if !planReferencesOuter(x.Plan, schema) {
		return evalExistsUncorrelated(x, env)
	}
	outer := make([]ir.OuterField, len(schema))
	for i, c := range schema {
		outer[i] = ir.OuterField{Qualifier: c.Qualifier, Name: c.Name, T: c.Type}
	}
	return &ir.ExistsExpr{Plan: x.Plan, OuterSchema: outer}, nil
}

// planReferencesOuter walks the plan tree and returns true if any
// expression contains a ColumnRef whose name+qualifier resolves
// against the outer schema. Uncorrelated subqueries answer false and
// take the pre-evaluated fast path.
func planReferencesOuter(n ir.Node, outer []Column) bool {
	if len(outer) == 0 {
		return false
	}
	found := false
	var walkExpr func(e ir.Expr)
	walkExpr = func(e ir.Expr) {
		if found {
			return
		}
		switch v := e.(type) {
		case *ir.ColumnRef:
			if _, err := resolveColumnRef(v, outer); err == nil {
				found = true
			}
		case *ir.BinOp:
			walkExpr(v.Left)
			walkExpr(v.Right)
		case *ir.UnaryOp:
			walkExpr(v.Expr)
		case *ir.FuncCall:
			for _, a := range v.Args {
				walkExpr(a)
			}
		case *ir.Cast:
			walkExpr(v.Expr)
		case *ir.Case:
			walkExpr(v.Operand)
			for _, w := range v.Whens {
				walkExpr(w.Match)
				walkExpr(w.Result)
			}
			walkExpr(v.Else)
		case *ir.InListExpr:
			walkExpr(v.Probe)
			for _, item := range v.List {
				walkExpr(item)
			}
		case *ir.AnyExpr:
			walkExpr(v.Probe)
			walkExpr(v.Array)
		}
	}
	var walk func(n ir.Node)
	walk = func(n ir.Node) {
		if found {
			return
		}
		switch x := n.(type) {
		case *ir.Filter:
			walkExpr(x.Cond)
			walk(x.Input)
		case *ir.Project:
			for _, e := range x.Exprs {
				walkExpr(e)
			}
			walk(x.Input)
		case *ir.Sort:
			for _, k := range x.Keys {
				walkExpr(k.Expr)
			}
			walk(x.Input)
		case *ir.Limit:
			walk(x.Input)
		case *ir.Aggregate:
			walk(x.Input)
		case *ir.Distinct:
			walk(x.Input)
		case *ir.Join:
			walk(x.Left)
			walk(x.Right)
			walkExpr(x.Cond)
		case *ir.SubqueryAlias:
			walk(x.Inner)
		case *ir.Union:
			walk(x.Left)
			walk(x.Right)
		case *ir.Window:
			walk(x.Input)
		}
	}
	walk(n)
	return found
}

// evalExistsUncorrelated runs the inner plan once and returns a
// constant Literal — the original fast path for an EXISTS that
// doesn't reference any outer-scope columns.
func evalExistsUncorrelated(x *ir.ExistsExpr, env *Env) (ir.Expr, error) {
	if env == nil {
		return nil, fmt.Errorf("exec: EXISTS requires execution environment")
	}
	op, err := Build(x.Plan, env)
	if err != nil {
		return nil, fmt.Errorf("EXISTS: %w", err)
	}
	defer op.Close()
	row, err := op.Next(context.Background())
	if errors.Is(err, io.EOF) {
		return &ir.Literal{Value: false, T: types.Bool}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("EXISTS: %w", err)
	}
	_ = row
	return &ir.Literal{Value: true, T: types.Bool}, nil
}

func resolveInList(x *ir.InListExpr, schema []Column, env *Env) (ir.Expr, error) {
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
}

func resolveInSubquery(x *ir.InSubqueryExpr, schema []Column, env *Env) (ir.Expr, error) {
	probe, err := resolveExpr(x.Probe, schema, env)
	if err != nil {
		return nil, err
	}
	list, err := evalInSubquery(x, env)
	if err != nil {
		return nil, err
	}
	return &ir.InListExpr{Probe: probe, List: list}, nil
}

func resolveAny(a *ir.AnyExpr, schema []Column, env *Env) (ir.Expr, error) {
	probe, err := resolveExpr(a.Probe, schema, env)
	if err != nil {
		return nil, err
	}
	arr, err := resolveExpr(a.Array, schema, env)
	if err != nil {
		return nil, err
	}
	return &ir.AnyExpr{Probe: probe, Op: a.Op, Array: arr}, nil
}

func resolveCast(c *ir.Cast, schema []Column, env *Env) (ir.Expr, error) {
	inner, err := resolveExpr(c.Expr, schema, env)
	if err != nil {
		return nil, err
	}
	return &ir.Cast{Expr: inner, T: c.T}, nil
}

// resolveCase recurses into the operand, every WHEN/THEN, and the
// optional ELSE. The result type is taken from the first non-nil-typed
// THEN — matching real PG's "first known type wins" rule for CASE
// branches that mix in NULLs.
func resolveCase(c *ir.Case, schema []Column, env *Env) (ir.Expr, error) {
	var operand ir.Expr
	if c.Operand != nil {
		op, err := resolveExpr(c.Operand, schema, env)
		if err != nil {
			return nil, err
		}
		operand = op
	}
	whens := make([]ir.CaseWhen, len(c.Whens))
	var resultT types.Type
	for i, w := range c.Whens {
		match, err := resolveExpr(w.Match, schema, env)
		if err != nil {
			return nil, err
		}
		result, err := resolveExpr(w.Result, schema, env)
		if err != nil {
			return nil, err
		}
		whens[i] = ir.CaseWhen{Match: match, Result: result}
		if resultT == nil {
			resultT = result.Type()
		}
	}
	var elseExpr ir.Expr
	if c.Else != nil {
		r, err := resolveExpr(c.Else, schema, env)
		if err != nil {
			return nil, err
		}
		elseExpr = r
		if resultT == nil {
			resultT = r.Type()
		}
	}
	return &ir.Case{Operand: operand, Whens: whens, Else: elseExpr, T: resultT}, nil
}

func envParams(env *Env) []Param {
	if env == nil {
		return nil
	}
	return env.Params
}

// resolveScalarSubquery is the resolveExpr-time entry. Uncorrelated
// subqueries pre-evaluate to a Literal (the original behaviour);
// correlated ones survive into evalExpr where the inner plan
// rebuilds per outer row.
func resolveScalarSubquery(x *ir.ScalarSubquery, schema []Column, env *Env) (ir.Expr, error) {
	if !planReferencesOuter(x.Plan, schema) {
		return evalScalarSubquery(x, env)
	}
	outer := make([]ir.OuterField, len(schema))
	for i, c := range schema {
		outer[i] = ir.OuterField{Qualifier: c.Qualifier, Name: c.Name, T: c.Type}
	}
	// Probe the inner plan once to learn its result type, so the
	// surrounding expression sees a non-nil Type. Build with the
	// outer schema set but with no per-row OuterRow yet — the type
	// only depends on the plan structure, not the row values.
	probeEnv := *env
	probeOuter := make([]Column, len(schema))
	copy(probeOuter, schema)
	probeEnv.OuterSchema = probeOuter
	probeEnv.OuterRow = make(Row, len(probeOuter))
	op, err := Build(x.Plan, &probeEnv)
	if err != nil {
		return nil, err
	}
	cols := op.OutputSchema()
	op.Close()
	if len(cols) != 1 {
		return nil, fmt.Errorf("exec: scalar subquery returned %d columns, want 1", len(cols))
	}
	return &ir.ScalarSubquery{Plan: x.Plan, T: cols[0].Type, OuterSchema: outer}, nil
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
		if x.Outer {
			if env == nil || x.Index < 0 || x.Index >= len(env.OuterRow) {
				return nil, fmt.Errorf("exec: outer column ref %q (idx %d) out of range", x.Name, x.Index)
			}
			return env.OuterRow[x.Index], nil
		}
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
	case *ir.Case:
		return evalCase(x, in, env)
	case *ir.AnyExpr:
		return evalAny(x, in, env)
	case *ir.ExistsExpr:
		return evalCorrelatedExists(x, in, env)
	case *ir.ScalarSubquery:
		return evalCorrelatedScalar(x, in, env)
	default:
		return nil, fmt.Errorf("exec: unsupported expr %T", e)
	}
}

// evalCorrelatedScalar runs the (correlated) inner plan with
// env.OuterRow set to the current outer row. Mirrors
// evalCorrelatedExists, but extracts a single value and enforces the
// "at most one row" rule.
func evalCorrelatedScalar(s *ir.ScalarSubquery, in Row, env *Env) (any, error) {
	outer := make([]Column, len(s.OuterSchema))
	for i, f := range s.OuterSchema {
		outer[i] = Column{Qualifier: f.Qualifier, Name: f.Name, Type: f.T}
	}
	childEnv := *env
	childEnv.OuterSchema = outer
	childEnv.OuterRow = in
	op, err := Build(s.Plan, &childEnv)
	if err != nil {
		return nil, fmt.Errorf("correlated scalar subquery: %w", err)
	}
	defer op.Close()
	row, err := op.Next(context.Background())
	if errors.Is(err, io.EOF) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("correlated scalar subquery: %w", err)
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
	return value, nil
}

// evalCorrelatedExists builds the inner plan with env.OuterSchema /
// OuterRow set so the inner operator's resolveColumnRef can fall
// back to outer columns and the per-row evaluator returns their
// current value. Uncorrelated EXISTS is pre-evaluated to a Literal
// during resolveExpr — only the correlated case lands here.
func evalCorrelatedExists(x *ir.ExistsExpr, in Row, env *Env) (any, error) {
	outer := make([]Column, len(x.OuterSchema))
	for i, f := range x.OuterSchema {
		outer[i] = Column{Qualifier: f.Qualifier, Name: f.Name, Type: f.T}
	}
	childEnv := *env
	childEnv.OuterSchema = outer
	childEnv.OuterRow = in
	op, err := Build(x.Plan, &childEnv)
	if err != nil {
		return nil, fmt.Errorf("correlated EXISTS: %w", err)
	}
	defer op.Close()
	row, err := op.Next(context.Background())
	if errors.Is(err, io.EOF) {
		return false, nil
	}
	if err != nil {
		return nil, fmt.Errorf("correlated EXISTS: %w", err)
	}
	_ = row
	return true, nil
}

// evalAny implements `probe op ANY (array)`. Currently only `=` is
// supported. Returns NULL if either operand is NULL or if the array
// is NULL; matches an element with NULL → ignored (PG: NULL element
// is "unknown").
func evalAny(a *ir.AnyExpr, in Row, env *Env) (any, error) {
	if a.Op != "=" {
		return nil, fmt.Errorf("exec: ANY only supports `=` for now, got %q", a.Op)
	}
	probe, err := evalExpr(a.Probe, in, env)
	if err != nil {
		return nil, err
	}
	arr, err := evalExpr(a.Array, in, env)
	if err != nil {
		return nil, err
	}
	if probe == nil || arr == nil {
		return nil, nil
	}
	matched := false
	switch v := arr.(type) {
	case []int64:
		for _, n := range v {
			cmp, err := compareValues(probe, n)
			if err != nil {
				return nil, err
			}
			if cmp == 0 {
				matched = true
				break
			}
		}
	case []int32:
		for _, n := range v {
			cmp, err := compareValues(probe, n)
			if err != nil {
				return nil, err
			}
			if cmp == 0 {
				matched = true
				break
			}
		}
	case []string:
		for _, s := range v {
			cmp, err := compareValues(probe, s)
			if err != nil {
				return nil, err
			}
			if cmp == 0 {
				matched = true
				break
			}
		}
	default:
		return nil, fmt.Errorf("exec: ANY: unsupported array type %T", arr)
	}
	return matched, nil
}

// evalCase walks the WHEN branches in order and returns the first
// matching THEN. For the simple form (Operand non-nil) the Match
// expression is compared to Operand for equality (NULL = anything is
// NULL, so those branches don't match). For the searched form (Operand
// nil) the Match must evaluate to true. Falls through to ELSE; if no
// branch matches and there is no ELSE the result is NULL.
func evalCase(c *ir.Case, in Row, env *Env) (any, error) {
	var operand any
	if c.Operand != nil {
		v, err := evalExpr(c.Operand, in, env)
		if err != nil {
			return nil, err
		}
		operand = v
	}
	for _, w := range c.Whens {
		match, err := evalExpr(w.Match, in, env)
		if err != nil {
			return nil, err
		}
		if c.Operand != nil {
			if operand == nil || match == nil {
				continue
			}
			cmp, err := compareValues(operand, match)
			if err != nil {
				return nil, err
			}
			if cmp == 0 {
				return evalExpr(w.Result, in, env)
			}
			continue
		}
		b, ok := match.(bool)
		if !ok {
			if match == nil {
				continue
			}
			return nil, fmt.Errorf("exec: CASE WHEN condition must be bool, got %T", match)
		}
		if b {
			return evalExpr(w.Result, in, env)
		}
	}
	if c.Else != nil {
		return evalExpr(c.Else, in, env)
	}
	return nil, nil
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
	case types.Int4Array, types.Int8Array, types.TextArray:
		// Arrays pass through as their already-decoded slice form
		// (the wire codec decodes parameters into the right Go slice
		// type before the cast runs).
		return v, nil
	case types.Float8:
		f, err := toFloat64(v)
		if err != nil {
			return nil, fmt.Errorf("cast to float8: %w", err)
		}
		return f, nil
	case types.Float4:
		f, err := toFloat64(v)
		if err != nil {
			return nil, fmt.Errorf("cast to float4: %w", err)
		}
		return float32(f), nil
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
	switch b.Op {
	case "is distinct from", "is not distinct from":
		return evalIsDistinctFrom(l, r, b.Op == "is not distinct from")
	}
	if l == nil || r == nil {
		return nil, nil
	}
	switch b.Op {
	case "+", "-", "*", "/", "%":
		return evalArith(b.Op, l, r, b.T)
	case "||":
		return evalConcat(l, r)
	case "like":
		return evalLike(l, r, false)
	case "ilike":
		return evalLike(l, r, true)
	case "->":
		return evalJSONArrow(l, r, false)
	case "->>":
		return evalJSONArrow(l, r, true)
	case "@>":
		return evalJSONContains(l, r)
	case "<@":
		return evalJSONContains(r, l)
	case "?":
		return evalJSONKeyExists(l, r)
	case "~":
		return evalRegex(l, r, false, false)
	case "~*":
		return evalRegex(l, r, true, false)
	case "!~":
		return evalRegex(l, r, false, true)
	case "!~*":
		return evalRegex(l, r, true, true)
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

// binOpResultType returns the static result type for a binary op given
// its resolved operand types. Most ops fall through to arithResultType,
// but a few (jsonb's `->` / `->>`) have a fixed result type independent
// of operand types.
func binOpResultType(op string, l, r types.Type) types.Type {
	switch op {
	case "->":
		return types.JSONB
	case "->>":
		return types.Text
	}
	return arithResultType(l, r)
}

func isFloatish(v any) bool {
	switch v.(type) {
	case float64, float32:
		return true
	}
	return false
}

func toFloat64(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case int32:
		return float64(n), nil
	case int:
		return float64(n), nil
	}
	return 0, fmt.Errorf("exec: not a number: %T", v)
}

func evalFloatArith(op string, l, r any) (any, error) {
	lf, err := toFloat64(l)
	if err != nil {
		return nil, err
	}
	rf, err := toFloat64(r)
	if err != nil {
		return nil, err
	}
	switch op {
	case "+":
		return lf + rf, nil
	case "-":
		return lf - rf, nil
	case "*":
		return lf * rf, nil
	case "/":
		if rf == 0 {
			return nil, &SQLError{Code: "22012", Message: "division by zero"}
		}
		return lf / rf, nil
	case "%":
		if rf == 0 {
			return nil, &SQLError{Code: "22012", Message: "division by zero"}
		}
		return math.Mod(lf, rf), nil
	}
	return nil, fmt.Errorf("exec: unsupported float arith op %q", op)
}

// tryTimeArith handles timestamp ± interval arithmetic. Returns the
// computed value with ok=true on a match, ok=false otherwise so
// evalArith falls through to the integer path.
func tryTimeArith(op string, l, r any) (any, bool) {
	if t, okT := l.(time.Time); okT {
		if d, okD := r.(time.Duration); okD {
			switch op {
			case "+":
				return t.Add(d), true
			case "-":
				return t.Add(-d), true
			}
		}
	}
	if d, okD := l.(time.Duration); okD {
		if t, okT := r.(time.Time); okT && op == "+" {
			return t.Add(d), true
		}
	}
	return nil, false
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
	if l == types.Timestamptz || r == types.Timestamptz {
		return types.Timestamptz
	}
	if l == types.Text || r == types.Text {
		return types.Text
	}
	if l == types.Float8 || r == types.Float8 {
		return types.Float8
	}
	if l == types.Float4 || r == types.Float4 {
		// Mixed float4/integer also widens to float8 in real PG
		// because integer literals are int4. Stay close to that.
		return types.Float8
	}
	if l == types.Int8 || r == types.Int8 {
		return types.Int8
	}
	return types.Int4
}

// evalIsDistinctFrom is NULL-safe equality. Per PG: NULL IS DISTINCT
// FROM NULL is false (they are *not* distinct); NULL IS DISTINCT FROM
// non-null is true. The negate flag flips the result for the
// IS NOT DISTINCT FROM form.
func evalIsDistinctFrom(l, r any, negate bool) (any, error) {
	var distinct bool
	switch {
	case l == nil && r == nil:
		distinct = false
	case l == nil || r == nil:
		distinct = true
	default:
		cmp, err := compareValues(l, r)
		if err != nil {
			return nil, err
		}
		distinct = cmp != 0
	}
	if negate {
		return !distinct, nil
	}
	return distinct, nil
}

// evalJSONArrow implements jsonb's `->` (asText=false) and `->>`
// (asText=true) operators. Either form takes a jsonb on the left and
// either a text key (for objects) or an integer index (for arrays) on
// the right. Missing keys / out-of-range indices yield NULL, matching
// real PG.
//
// `->` re-marshals the extracted value back to jsonb bytes.
// `->>` returns text: strings unwrap to their value, scalars are
// formatted with Go's default JSON-style stringification, and JSON
// null returns NULL (not the literal "null").
func evalJSONArrow(l, r any, asText bool) (any, error) {
	raw, ok := l.([]byte)
	if !ok {
		return nil, fmt.Errorf("exec: jsonb arrow: left operand must be jsonb, got %T", l)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("exec: jsonb arrow: invalid jsonb: %w", err)
	}
	got, found := jsonArrowSelect(doc, r)
	if !found {
		return nil, nil
	}
	if asText {
		if got == nil {
			return nil, nil
		}
		if s, ok := got.(string); ok {
			return s, nil
		}
		out, err := json.Marshal(got)
		if err != nil {
			return nil, fmt.Errorf("exec: jsonb ->>: %w", err)
		}
		return string(out), nil
	}
	out, err := json.Marshal(got)
	if err != nil {
		return nil, fmt.Errorf("exec: jsonb ->: %w", err)
	}
	return out, nil
}

// jsonArrowSelect resolves the `->` / `->>` index against the decoded
// jsonb value. Returns (value, true) on success — value may be nil for
// JSON null. (nil, false) means "no such key/index", which surfaces as
// SQL NULL.
func jsonArrowSelect(doc any, idx any) (any, bool) {
	switch d := doc.(type) {
	case map[string]any:
		key, ok := idx.(string)
		if !ok {
			return nil, false
		}
		v, ok := d[key]
		return v, ok
	case []any:
		var i int
		switch n := idx.(type) {
		case int32:
			i = int(n)
		case int64:
			i = int(n)
		case int:
			i = n
		default:
			return nil, false
		}
		if i < 0 {
			i += len(d)
		}
		if i < 0 || i >= len(d) {
			return nil, false
		}
		return d[i], true
	}
	return nil, false
}

// evalJSONKeyExists implements jsonb's `?` key-exists operator: true
// iff the right (text) appears as a top-level key (objects), as an
// element (arrays), or as the value itself (scalar string).
func evalJSONKeyExists(l, r any) (any, error) {
	rawL, ok := l.([]byte)
	if !ok {
		return nil, fmt.Errorf("exec: jsonb ?: left must be jsonb, got %T", l)
	}
	key, ok := r.(string)
	if !ok {
		return nil, fmt.Errorf("exec: jsonb ?: right must be text, got %T", r)
	}
	var doc any
	if err := json.Unmarshal(rawL, &doc); err != nil {
		return nil, fmt.Errorf("exec: jsonb ?: invalid jsonb: %w", err)
	}
	switch d := doc.(type) {
	case map[string]any:
		_, present := d[key]
		return present, nil
	case []any:
		for _, e := range d {
			if s, ok := e.(string); ok && s == key {
				return true, nil
			}
		}
		return false, nil
	case string:
		return d == key, nil
	}
	return false, nil
}

// evalJSONContains implements jsonb's `@>` containment: returns true
// iff every key/value (objects) or every element (arrays) from `right`
// is present in `left`. Scalars compare for equality. Both operands
// must be jsonb (raw bytes).
func evalJSONContains(l, r any) (any, error) {
	rawL, ok := l.([]byte)
	if !ok {
		return nil, fmt.Errorf("exec: jsonb @>: left must be jsonb, got %T", l)
	}
	rawR, ok := r.([]byte)
	if !ok {
		return nil, fmt.Errorf("exec: jsonb @>: right must be jsonb, got %T", r)
	}
	var lv, rv any
	if err := json.Unmarshal(rawL, &lv); err != nil {
		return nil, fmt.Errorf("exec: jsonb @>: invalid jsonb on left: %w", err)
	}
	if err := json.Unmarshal(rawR, &rv); err != nil {
		return nil, fmt.Errorf("exec: jsonb @>: invalid jsonb on right: %w", err)
	}
	return jsonContains(lv, rv), nil
}

// jsonContains is the recursive containment predicate. PG's exact
// rules are richer (e.g. an array contains a scalar element if any
// member equals it); this implementation matches the common cases.
func jsonContains(l, r any) bool {
	switch rv := r.(type) {
	case map[string]any:
		lm, ok := l.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range rv {
			lv, present := lm[k]
			if !present || !jsonContains(lv, v) {
				return false
			}
		}
		return true
	case []any:
		la, ok := l.([]any)
		if !ok {
			return false
		}
		for _, want := range rv {
			found := false
			for _, got := range la {
				if jsonContains(got, want) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	default:
		// Scalar: equality. Use json.Marshal to canonicalize so 1 ≠ 1.0
		// noise is contained — the byte forms match when JSON values
		// are equivalent at the JSON level.
		lb, err1 := json.Marshal(l)
		rb, err2 := json.Marshal(r)
		if err1 != nil || err2 != nil {
			return false
		}
		return string(lb) == string(rb)
	}
}

// evalRegex implements PG's `~`, `~*`, `!~`, `!~*` regex match
// operators. Patterns compile per-call — caching by Expr identity is
// a follow-up. Compilation errors surface to the caller; PG would
// raise SQLSTATE 22023 here, but our tests don't yet inspect the
// code so a plain error suffices.
func evalRegex(l, r any, ignoreCase, negate bool) (any, error) {
	s, ok := l.(string)
	if !ok {
		return nil, fmt.Errorf("exec: regex left operand must be text, got %T", l)
	}
	p, ok := r.(string)
	if !ok {
		return nil, fmt.Errorf("exec: regex pattern must be text, got %T", r)
	}
	if ignoreCase {
		p = "(?i)" + p
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, fmt.Errorf("exec: invalid regex %q: %w", p, err)
	}
	matched := re.MatchString(s)
	if negate {
		return !matched, nil
	}
	return matched, nil
}

// evalLike implements PG's LIKE / ILIKE pattern matching: `_` matches
// any single char, `%` matches any (possibly empty) substring, and `\`
// escapes the next char in the pattern (so `\%` is a literal `%`).
// ILIKE folds to lower-case before matching — fine for ASCII; full
// Unicode case folding is a follow-up.
func evalLike(l, r any, fold bool) (any, error) {
	s, ok := l.(string)
	if !ok {
		return nil, fmt.Errorf("exec: LIKE left operand must be text, got %T", l)
	}
	pat, ok := r.(string)
	if !ok {
		return nil, fmt.Errorf("exec: LIKE pattern must be text, got %T", r)
	}
	if fold {
		s = strings.ToLower(s)
		pat = strings.ToLower(pat)
	}
	return likeMatch(s, pat), nil
}

// likeMatch is a straightforward recursive matcher: `%` consumes
// substrings via a tail-search, `_` consumes one char, `\` escapes the
// next pattern char. We accept zero-width matches like real PG.
func likeMatch(s, pat string) bool {
	for i := 0; i < len(pat); i++ {
		c := pat[i]
		switch c {
		case '%':
			// Coalesce runs of `%` so we don't recurse needlessly.
			for i+1 < len(pat) && pat[i+1] == '%' {
				i++
			}
			rest := pat[i+1:]
			if rest == "" {
				return true
			}
			for j := 0; j <= len(s); j++ {
				if likeMatch(s[j:], rest) {
					return true
				}
			}
			return false
		case '_':
			if len(s) == 0 {
				return false
			}
			s = s[1:]
		case '\\':
			if i+1 >= len(pat) {
				return false
			}
			i++
			if len(s) == 0 || s[0] != pat[i] {
				return false
			}
			s = s[1:]
		default:
			if len(s) == 0 || s[0] != c {
				return false
			}
			s = s[1:]
		}
	}
	return len(s) == 0
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
	if v, ok := tryTimeArith(op, l, r); ok {
		return v, nil
	}
	if isFloatish(l) || isFloatish(r) || resultT == types.Float8 || resultT == types.Float4 {
		return evalFloatArith(op, l, r)
	}
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
	case "is null":
		return v == nil, nil
	case "is not null":
		return v != nil, nil
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
	if isFloatish(a) || isFloatish(b) {
		af, err := toFloat64(a)
		if err != nil {
			return 0, err
		}
		bf, err := toFloat64(b)
		if err != nil {
			return 0, err
		}
		switch {
		case af < bf:
			return -1, nil
		case af > bf:
			return 1, nil
		}
		return 0, nil
	}
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
