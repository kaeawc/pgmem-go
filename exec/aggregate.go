package exec

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/types"
)

// buildAggregate compiles an Aggregate IR node. We resolve each call's
// argument against the input schema (so column refs see the underlying
// row layout) and dispatch by name to a specific accumulator factory.
//
// Result types:
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

	cols := make([]Column, len(p.Calls))
	accs := make([]aggAcc, len(p.Calls))
	for i, call := range p.Calls {
		var arg ir.Expr
		if call.Arg != nil {
			arg, err = resolveExpr(call.Arg, inSchema, env)
			if err != nil {
				in.Close()
				return nil, err
			}
		}
		acc, err := newAggregator(call.Func, arg)
		if err != nil {
			in.Close()
			return nil, err
		}
		accs[i] = acc
		name := call.Output
		if name == "" {
			name = call.Func
		}
		cols[i] = Column{Name: name, Type: acc.resultType()}
	}
	return &aggregateOp{in: in, accs: accs, cols: cols, env: env}, nil
}

// aggAcc abstracts an accumulator: feed rows in, get the result row.
// Each call gets its own instance.
type aggAcc interface {
	resultType() types.Type
	accept(in Row, env *Env) error
	result() (any, error)
}

type aggregateOp struct {
	in   Operator
	accs []aggAcc
	cols []Column
	env  *Env

	done bool
}

func (a *aggregateOp) OutputSchema() []Column { return a.cols }
func (a *aggregateOp) Close() error           { return a.in.Close() }

func (a *aggregateOp) Next(ctx context.Context) (Row, error) {
	if a.done {
		return nil, io.EOF
	}
	a.done = true
	for {
		row, err := a.in.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		for _, acc := range a.accs {
			if err := acc.accept(row, a.env); err != nil {
				return nil, err
			}
		}
	}
	out := make(Row, len(a.accs))
	for i, acc := range a.accs {
		v, err := acc.result()
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func newAggregator(name string, arg ir.Expr) (aggAcc, error) {
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
	}
	return nil, fmt.Errorf("exec: unknown aggregate %q", name)
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
