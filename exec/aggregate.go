package exec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/types"
)

// buildAggregate compiles an Aggregate IR node.
//
//   - Scalar (no GroupBy): one accumulator per call, single output row.
//   - Grouped: input rows hash by tuple of GroupBy values; per group
//     we keep a fresh set of accumulators. Output schema is
//     [groupKey…, aggCall…] in that order; the planning Project
//     wraps Aggregate to surface that to the user's SELECT list.
//
// Result types of aggregates:
//
//	COUNT     → int8 (PG's bigint, matches what pgx scans into int64)
//	SUM(int4) → int8 ; SUM(int8) → int8
//	AVG(int)  → int8 (integer-only divisor today; numeric arrives later)
//	MIN/MAX   → input column type
func buildAggregate(p *ir.Aggregate, env *Env) (Operator, error) {
	in, err := Build(p.Input, env)
	if err != nil {
		return nil, err
	}
	inSchema := in.OutputSchema()

	groupKeys := make([]ir.Expr, len(p.GroupBy))
	groupCols := make([]Column, len(p.GroupBy))
	for i, e := range p.GroupBy {
		resolved, err := resolveExpr(e, inSchema, env)
		if err != nil {
			in.Close()
			return nil, err
		}
		groupKeys[i] = resolved
		// Group output columns inherit the original column name and
		// type when the GROUP BY entry is a bare ColumnRef so the
		// planning Project can rewire them by name. Arbitrary
		// expressions get a synthetic name matching the parser-side
		// rewriter.
		if c, ok := e.(*ir.ColumnRef); ok {
			groupCols[i] = Column{Qualifier: c.Qualifier, Name: c.Name, Type: resolved.Type()}
		} else {
			groupCols[i] = Column{Name: fmt.Sprintf("__group_%d", i), Type: resolved.Type()}
		}
	}

	resolvedArgs := make([][]ir.Expr, len(p.Calls))
	for i, call := range p.Calls {
		resolvedArgs[i] = make([]ir.Expr, len(call.Args))
		for j, a := range call.Args {
			r, err := resolveExpr(a, inSchema, env)
			if err != nil {
				in.Close()
				return nil, err
			}
			resolvedArgs[i][j] = r
		}
	}
	// Probe accumulator types via a throwaway accumulator set — the
	// real per-group accs are constructed lazily during Next.
	probe, err := newAccumulatorSet(p.Calls, resolvedArgs)
	if err != nil {
		in.Close()
		return nil, err
	}
	aggCols := make([]Column, len(p.Calls))
	for i := range p.Calls {
		name := p.Calls[i].Output
		if name == "" {
			name = p.Calls[i].Func
		}
		aggCols[i] = Column{Name: name, Type: probe[i].resultType()}
	}

	cols := append(groupCols, aggCols...)
	return &aggregateOp{
		in:        in,
		calls:     p.Calls,
		callArgs:  resolvedArgs,
		groupKeys: groupKeys,
		cols:      cols,
		env:       env,
	}, nil
}

// newAccumulatorSet builds one accumulator per call. resolvedArgs[i]
// is the resolved argument list for calls[i] — empty for COUNT(*).
func newAccumulatorSet(calls []ir.AggregateCall, resolvedArgs [][]ir.Expr) ([]aggAcc, error) {
	out := make([]aggAcc, len(calls))
	for i, call := range calls {
		acc, err := newAggregator(call.Func, resolvedArgs[i])
		if err != nil {
			return nil, err
		}
		if call.Distinct && len(resolvedArgs[i]) > 0 {
			acc = &distinctAggWrap{
				inner: acc,
				args:  resolvedArgs[i],
				seen:  map[string]struct{}{},
			}
		}
		out[i] = acc
	}
	return out, nil
}

// distinctAggWrap dedupes incoming rows by the argument tuple before
// forwarding them to the inner accumulator. Duplicates among the
// already-seen tuples are dropped silently.
type distinctAggWrap struct {
	inner aggAcc
	args  []ir.Expr
	seen  map[string]struct{}
}

func (d *distinctAggWrap) resultType() types.Type { return d.inner.resultType() }

func (d *distinctAggWrap) accept(in Row, env *Env) error {
	parts := make([]string, len(d.args))
	for i, a := range d.args {
		v, err := evalExpr(a, in, env)
		if err != nil {
			return err
		}
		parts[i] = uniqueKey(v)
	}
	key := strings.Join(parts, "\x00")
	if _, dup := d.seen[key]; dup {
		return nil
	}
	d.seen[key] = struct{}{}
	return d.inner.accept(in, env)
}

func (d *distinctAggWrap) result() (any, error) { return d.inner.result() }

// aggAcc abstracts an accumulator: feed rows in, get the result row.
// Each call gets its own instance.
type aggAcc interface {
	resultType() types.Type
	accept(in Row, env *Env) error
	result() (any, error)
}

type aggregateOp struct {
	in        Operator
	calls     []ir.AggregateCall
	callArgs  [][]ir.Expr // parallel to calls; empty for COUNT(*)
	groupKeys []ir.Expr   // resolved against input schema; empty for scalar agg
	cols      []Column
	env       *Env

	// Lazy state: filled on first Next, drained over subsequent Next
	// calls. For scalar aggregation pending has exactly one entry; for
	// grouped aggregation it's keyed by the tuple of groupKey values
	// stringified through uniqueKey.
	ran     bool
	pending []Row
	pos     int
}

func (a *aggregateOp) OutputSchema() []Column { return a.cols }
func (a *aggregateOp) Close() error           { return a.in.Close() }

func (a *aggregateOp) Next(ctx context.Context) (Row, error) {
	_ = ctx
	if !a.ran {
		if err := a.runOnce(); err != nil {
			return nil, err
		}
	}
	if a.pos >= len(a.pending) {
		return nil, io.EOF
	}
	r := a.pending[a.pos]
	a.pos++
	return r, nil
}

// runOnce drains the input, partitions by the group key tuple, and
// computes one output row per group (or one row total when there's no
// GROUP BY — that's the scalar-aggregate case where we still emit a
// row even if input was empty).
func (a *aggregateOp) runOnce() error {
	a.ran = true
	type bucket struct {
		keys []any // values of the GROUP BY exprs for this group
		accs []aggAcc
	}
	groups := map[string]*bucket{}
	order := []string{} // preserve insertion order for stable output

	getOrCreate := func(key string, keys []any) (*bucket, error) {
		if b, ok := groups[key]; ok {
			return b, nil
		}
		accs, err := newAccumulatorSet(a.calls, a.callArgs)
		if err != nil {
			return nil, err
		}
		b := &bucket{keys: keys, accs: accs}
		groups[key] = b
		order = append(order, key)
		return b, nil
	}

	ctx := context.Background()
	for {
		row, err := a.in.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		keys := make([]any, len(a.groupKeys))
		for i, e := range a.groupKeys {
			v, err := evalExpr(e, row, a.env)
			if err != nil {
				return err
			}
			keys[i] = v
		}
		b, err := getOrCreate(groupKeyString(keys), keys)
		if err != nil {
			return err
		}
		for _, acc := range b.accs {
			if err := acc.accept(row, a.env); err != nil {
				return err
			}
		}
	}

	// Scalar aggregate (no GROUP BY): always emit a single row, even
	// when the input was empty — matches PG's COUNT-on-empty-table = 0.
	if len(a.groupKeys) == 0 && len(groups) == 0 {
		accs, err := newAccumulatorSet(a.calls, a.callArgs)
		if err != nil {
			return err
		}
		groups[""] = &bucket{accs: accs}
		order = append(order, "")
	}

	a.pending = make([]Row, 0, len(order))
	for _, k := range order {
		b := groups[k]
		out := make(Row, 0, len(b.keys)+len(b.accs))
		out = append(out, b.keys...)
		for _, acc := range b.accs {
			v, err := acc.result()
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		a.pending = append(a.pending, out)
	}
	return nil
}

// groupKeyString produces a stable string key for a tuple of group
// values, using the same uniqueKey-style typing the unique constraint
// already relies on.
func groupKeyString(vals []any) string {
	if len(vals) == 0 {
		return ""
	}
	parts := make([]string, len(vals))
	for i, v := range vals {
		if v == nil {
			parts[i] = "<null>"
			continue
		}
		parts[i] = uniqueKey(v)
	}
	// Use a separator unlikely to collide with type-prefixed payloads.
	return strings.Join(parts, "\x00")
}

func newAggregator(name string, args []ir.Expr) (aggAcc, error) {
	var arg ir.Expr
	if len(args) > 0 {
		arg = args[0]
	}
	switch name {
	case "count":
		return &countAgg{arg: arg}, nil
	case "sum":
		return &sumAgg{arg: arg}, nil
	case "min":
		return &minMaxAgg{arg: arg, isMax: false}, nil
	case "max":
		return &minMaxAgg{arg: arg, isMax: true}, nil
	case "avg":
		return &avgAgg{arg: arg}, nil
	case "string_agg":
		if len(args) != 2 {
			return nil, fmt.Errorf("exec: string_agg takes 2 arguments, got %d", len(args))
		}
		return &stringAggAcc{value: args[0], sep: args[1]}, nil
	case "bool_and", "every":
		return &boolAgg{arg: arg, all: true}, nil
	case "bool_or":
		return &boolAgg{arg: arg, all: false}, nil
	}
	return nil, fmt.Errorf("exec: unknown aggregate %q", name)
}

// boolAgg implements bool_and (all=true) and bool_or (all=false). PG
// rules: NULL input is skipped; if every input is NULL the result is
// NULL. bool_and returns true iff every observed value is true;
// bool_or returns true iff at least one observed value is true.
type boolAgg struct {
	arg ir.Expr
	all bool
	any bool
	val bool // running accumulator: starts true for AND, false for OR
}

func (b *boolAgg) resultType() types.Type { return types.Bool }

func (b *boolAgg) accept(in Row, env *Env) error {
	v, err := evalExpr(b.arg, in, env)
	if err != nil {
		return err
	}
	if v == nil {
		return nil
	}
	bv, ok := v.(bool)
	if !ok {
		return fmt.Errorf("exec: bool aggregate: expected bool, got %T", v)
	}
	if !b.any {
		b.any = true
		b.val = bv
		return nil
	}
	if b.all {
		b.val = b.val && bv
	} else {
		b.val = b.val || bv
	}
	return nil
}

func (b *boolAgg) result() (any, error) {
	if !b.any {
		return nil, nil
	}
	return b.val, nil
}

type stringAggAcc struct {
	value ir.Expr
	sep   ir.Expr
	parts []string
	seps  []string
	any   bool
}

func (s *stringAggAcc) resultType() types.Type { return types.Text }

func (s *stringAggAcc) accept(in Row, env *Env) error {
	v, err := evalExpr(s.value, in, env)
	if err != nil {
		return err
	}
	if v == nil {
		return nil
	}
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("exec: string_agg value must be text, got %T", v)
	}
	sepVal, err := evalExpr(s.sep, in, env)
	if err != nil {
		return err
	}
	sep, ok := sepVal.(string)
	if !ok {
		// NULL separator collapses to "" — same as PG when the column
		// providing the separator yields NULL.
		sep = ""
	}
	s.parts = append(s.parts, str)
	s.seps = append(s.seps, sep)
	s.any = true
	return nil
}

func (s *stringAggAcc) result() (any, error) {
	if !s.any {
		return nil, nil
	}
	var b strings.Builder
	for i, p := range s.parts {
		if i > 0 {
			// Use the separator captured *with the second value*; PG uses
			// the separator from any matching row (it's typically a
			// constant), but takes the per-row value when it varies.
			b.WriteString(s.seps[i])
		}
		b.WriteString(p)
	}
	return b.String(), nil
}

// --- count ---
//
// nil arg = COUNT(*); count every row. Non-nil arg = COUNT(expr); skip
// rows whose evaluated arg is NULL (matching PG).
type countAgg struct {
	arg ir.Expr
	n   int64
}

func (c *countAgg) resultType() types.Type { return types.Int8 }

func (c *countAgg) accept(in Row, env *Env) error {
	if c.arg == nil {
		c.n++
		return nil
	}
	v, err := evalExpr(c.arg, in, env)
	if err != nil {
		return err
	}
	if v != nil {
		c.n++
	}
	return nil
}

func (c *countAgg) result() (any, error) { return c.n, nil }

// --- sum ---
//
// Integer-only: int4 / int8 promoted to int64. PG returns NUMERIC for
// int8 sums to avoid overflow; we return int8 with documented overflow
// risk because numeric isn't in the type kit yet.
type sumAgg struct {
	arg    ir.Expr
	sum    int64
	hasAny bool // PG returns NULL when the input is empty / all-NULL
}

func (s *sumAgg) resultType() types.Type { return types.Int8 }

func (s *sumAgg) accept(in Row, env *Env) error {
	v, err := evalExpr(s.arg, in, env)
	if err != nil {
		return err
	}
	if v == nil {
		return nil
	}
	n, err := toInt64(v)
	if err != nil {
		return err
	}
	s.sum += n
	s.hasAny = true
	return nil
}

func (s *sumAgg) result() (any, error) {
	if !s.hasAny {
		return nil, nil
	}
	return s.sum, nil
}

// --- min / max ---
//
// Polymorphic on the column type — works for any type compareValues
// supports (int, text, bool, uuid, timestamptz, bytea). The result
// type echoes the input column's type so RETURNING/output schema is
// correct downstream.
type minMaxAgg struct {
	arg   ir.Expr
	isMax bool
	best  any
	t     types.Type
}

func (m *minMaxAgg) resultType() types.Type {
	if m.t != nil {
		return m.t
	}
	if m.arg != nil {
		return m.arg.Type()
	}
	return nil
}

func (m *minMaxAgg) accept(in Row, env *Env) error {
	v, err := evalExpr(m.arg, in, env)
	if err != nil {
		return err
	}
	if v == nil {
		return nil
	}
	if m.t == nil {
		m.t = m.arg.Type()
	}
	if m.best == nil {
		m.best = v
		return nil
	}
	cmp, err := compareValues(v, m.best)
	if err != nil {
		return err
	}
	if (m.isMax && cmp > 0) || (!m.isMax && cmp < 0) {
		m.best = v
	}
	return nil
}

func (m *minMaxAgg) result() (any, error) { return m.best, nil }

// --- avg ---
//
// Integer-only for now. AVG returns int8 truncated division; PG
// would return numeric. The result is NULL when the input is empty
// or all-NULL.
type avgAgg struct {
	arg ir.Expr
	sum int64
	n   int64
}

func (a *avgAgg) resultType() types.Type { return types.Int8 }

func (a *avgAgg) accept(in Row, env *Env) error {
	v, err := evalExpr(a.arg, in, env)
	if err != nil {
		return err
	}
	if v == nil {
		return nil
	}
	n, err := toInt64(v)
	if err != nil {
		return err
	}
	a.sum += n
	a.n++
	return nil
}

func (a *avgAgg) result() (any, error) {
	if a.n == 0 {
		return nil, nil
	}
	return a.sum / a.n, nil
}
