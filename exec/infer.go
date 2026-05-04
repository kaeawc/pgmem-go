package exec

import (
	"github.com/kaeawc/pgmem-go/catalog"
	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/types"
)

// InferParamTypes walks an IR plan and determines a type for each
// ParamRef from context. Declared (from Parse OIDs) takes precedence
// over inferred — the client knows what it sent.
//
// Slots we couldn't infer default to types.Text; the client will
// encode parameters as text and our decoders cope.
//
// This is the one place that bridges parse and exec without going
// through Build. The wire layer calls it between Parse and Describe.
func InferParamTypes(plan ir.Node, sch catalog.Schema, declared []types.Type) []types.Type {
	maxIdx := -1
	hint := map[int]types.Type{}
	walkParams(plan, sch, "", hint, &maxIdx)

	size := len(declared)
	if maxIdx+1 > size {
		size = maxIdx + 1
	}
	out := make([]types.Type, size)
	for i := range out {
		switch {
		case i < len(declared) && declared[i] != nil:
			out[i] = declared[i]
		case hint[i] != nil:
			out[i] = hint[i]
		default:
			out[i] = types.Text
		}
	}
	return out
}

// walkParams traverses the plan tree depth-first. scopeTable is the
// most recently encountered Scan's table name; it lets BinOp inference
// look up ColumnRef types against the catalog without needing the full
// resolveExpr machinery.
//
// Per-node logic lives in walk* helpers below so this dispatch stays
// short; gocyclo otherwise inflates with every new IR node we add.
func walkParams(n ir.Node, sch catalog.Schema, scopeTable string, hint map[int]types.Type, maxIdx *int) {
	switch p := n.(type) {
	case *ir.Project:
		walkParamsProject(p, sch, scopeTable, hint, maxIdx)
	case *ir.Filter:
		walkParamsFilter(p, sch, scopeTable, hint, maxIdx)
	case *ir.Join:
		walkParamsJoin(p, sch, scopeTable, hint, maxIdx)
	case *ir.Sort:
		walkParamsSort(p, sch, scopeTable, hint, maxIdx)
	case *ir.Limit:
		walkParamsLimit(p, sch, scopeTable, hint, maxIdx)
	case *ir.Aggregate:
		next := scopeFor(p.Input, scopeTable)
		walkParams(p.Input, sch, next, hint, maxIdx)
		for _, c := range p.Calls {
			if c.Arg != nil {
				walkExprParams(c.Arg, nil, sch, next, hint, maxIdx)
			}
		}
	case *ir.Distinct:
		walkParams(p.Input, sch, scopeFor(p.Input, scopeTable), hint, maxIdx)
	case *ir.Insert:
		walkParamsInsert(p, sch, scopeTable, hint, maxIdx)
	case *ir.Delete:
		walkParamsDelete(p, sch, hint, maxIdx)
	case *ir.Update:
		walkParamsUpdate(p, sch, hint, maxIdx)
	case *ir.Values:
		walkParamsValues(p, sch, scopeTable, hint, maxIdx)
	case *ir.Scan, *ir.CreateTable, nil:
		// Leaves carry no expressions; nothing to record.
	}
}

func walkParamsProject(p *ir.Project, sch catalog.Schema, scopeTable string, hint map[int]types.Type, maxIdx *int) {
	next := scopeFor(p.Input, scopeTable)
	walkParams(p.Input, sch, next, hint, maxIdx)
	for _, e := range p.Exprs {
		walkExprParams(e, nil, sch, next, hint, maxIdx)
	}
}

func walkParamsFilter(p *ir.Filter, sch catalog.Schema, scopeTable string, hint map[int]types.Type, maxIdx *int) {
	next := scopeFor(p.Input, scopeTable)
	walkParams(p.Input, sch, next, hint, maxIdx)
	walkExprParams(p.Cond, nil, sch, next, hint, maxIdx)
}

// walkParamsJoin descends both sides. The ON condition uses qualified
// refs the executor resolves against the combined schema; bare refs on
// either side fall back to the type-from-the-other-side propagation in
// walkExprParams.
func walkParamsJoin(p *ir.Join, sch catalog.Schema, scopeTable string, hint map[int]types.Type, maxIdx *int) {
	walkParams(p.Left, sch, scopeFor(p.Left, scopeTable), hint, maxIdx)
	walkParams(p.Right, sch, scopeFor(p.Right, scopeTable), hint, maxIdx)
	if p.Cond != nil {
		walkExprParams(p.Cond, nil, sch, scopeTable, hint, maxIdx)
	}
}

func walkParamsSort(p *ir.Sort, sch catalog.Schema, scopeTable string, hint map[int]types.Type, maxIdx *int) {
	next := scopeFor(p.Input, scopeTable)
	walkParams(p.Input, sch, next, hint, maxIdx)
	for _, k := range p.Keys {
		walkExprParams(k.Expr, nil, sch, next, hint, maxIdx)
	}
}

func walkParamsLimit(p *ir.Limit, sch catalog.Schema, scopeTable string, hint map[int]types.Type, maxIdx *int) {
	next := scopeFor(p.Input, scopeTable)
	walkParams(p.Input, sch, next, hint, maxIdx)
	if p.Count != nil {
		walkExprParams(p.Count, types.Int8, sch, next, hint, maxIdx)
	}
	if p.Offset != nil {
		walkExprParams(p.Offset, types.Int8, sch, next, hint, maxIdx)
	}
}

func walkParamsInsert(p *ir.Insert, sch catalog.Schema, scopeTable string, hint map[int]types.Type, maxIdx *int) {
	ct, ok := sch.Table(p.Table)
	if !ok {
		return
	}
	colMap := insertColMap(ct, p.Columns)
	for _, row := range p.Rows {
		for j, e := range row {
			var expected types.Type
			if j < len(colMap) && colMap[j] >= 0 && colMap[j] < len(ct.Columns) {
				expected = ct.Columns[colMap[j]].Type
			}
			walkExprParams(e, expected, sch, scopeTable, hint, maxIdx)
		}
	}
}

// walkParamsDelete: WHERE and RETURNING expressions both reference the
// target table's columns, so scope is the table itself.
func walkParamsDelete(p *ir.Delete, sch catalog.Schema, hint map[int]types.Type, maxIdx *int) {
	if p.Where != nil {
		walkExprParams(p.Where, nil, sch, p.Table, hint, maxIdx)
	}
	for _, e := range p.Returning {
		walkExprParams(e, nil, sch, p.Table, hint, maxIdx)
	}
}

func walkParamsUpdate(p *ir.Update, sch catalog.Schema, hint map[int]types.Type, maxIdx *int) {
	ct, ok := sch.Table(p.Table)
	var colTypes map[string]types.Type
	if ok {
		colTypes = make(map[string]types.Type, len(ct.Columns))
		for _, c := range ct.Columns {
			colTypes[c.Name] = c.Type
		}
	}
	for _, a := range p.Assignments {
		// `col = $N` constrains $N to col's type.
		walkExprParams(a.Expr, colTypes[a.Column], sch, p.Table, hint, maxIdx)
	}
	if p.Where != nil {
		walkExprParams(p.Where, nil, sch, p.Table, hint, maxIdx)
	}
	for _, e := range p.Returning {
		walkExprParams(e, nil, sch, p.Table, hint, maxIdx)
	}
}

func walkParamsValues(p *ir.Values, sch catalog.Schema, scopeTable string, hint map[int]types.Type, maxIdx *int) {
	for _, row := range p.Rows {
		for _, e := range row {
			walkExprParams(e, nil, sch, scopeTable, hint, maxIdx)
		}
	}
}

// scopeFor extracts the underlying table for a child plan node by
// walking through pure-relational wrappers. Stops at the first Scan.
// A Join straddles two tables — there isn't a single scope to fall
// back to — so we return the fallback in that case and rely on
// qualified column refs for resolution.
func scopeFor(n ir.Node, fallback string) string {
	for {
		switch x := n.(type) {
		case *ir.Scan:
			return x.Table
		case *ir.Project:
			n = x.Input
		case *ir.Filter:
			n = x.Input
		case *ir.Sort:
			n = x.Input
		case *ir.Limit:
			n = x.Input
		case *ir.Join:
			return fallback
		default:
			return fallback
		}
	}
}

func insertColMap(ct catalog.Table, cols []string) []int {
	if len(cols) == 0 {
		out := make([]int, len(ct.Columns))
		for i := range out {
			out[i] = i
		}
		return out
	}
	out := make([]int, len(cols))
	for i, name := range cols {
		out[i] = -1
		for j, c := range ct.Columns {
			if c.Name == name {
				out[i] = j
				break
			}
		}
	}
	return out
}

// walkExprParams is the expression-side of inference. expected is the
// type the surrounding context demands (e.g. the column type for an
// INSERT VALUES position, or the other side of a comparison).
func walkExprParams(e ir.Expr, expected types.Type, sch catalog.Schema, scopeTable string, hint map[int]types.Type, maxIdx *int) {
	switch x := e.(type) {
	case *ir.ParamRef:
		if x.Index > *maxIdx {
			*maxIdx = x.Index
		}
		if expected != nil {
			if cur, ok := hint[x.Index]; !ok || cur == types.Text {
				hint[x.Index] = expected
			}
		}
	case *ir.BinOp:
		// For comparisons, propagate the static side's type to the parameter
		// side. Boolean operators (AND/OR) don't constrain operand types
		// beyond bool, but their result type IS bool — which we use only
		// when the operand is itself a ParamRef (rare).
		lt := exprStaticType(x.Left, sch, scopeTable)
		rt := exprStaticType(x.Right, sch, scopeTable)
		walkExprParams(x.Left, rt, sch, scopeTable, hint, maxIdx)
		walkExprParams(x.Right, lt, sch, scopeTable, hint, maxIdx)
	case *ir.UnaryOp:
		walkExprParams(x.Expr, expected, sch, scopeTable, hint, maxIdx)
	case *ir.FuncCall:
		// We don't know the arg types of an arbitrary builtin from here,
		// so descend without an `expected` hint. Specific functions can
		// constrain types via dedicated cases when that matters.
		for _, a := range x.Args {
			walkExprParams(a, nil, sch, scopeTable, hint, maxIdx)
		}
	case *ir.InListExpr:
		// `probe IN (a, b, ...)`: probe and each list element propagate
		// types to one another. We don't know which side carries the
		// concrete type at parse time, so descend with no expected hint
		// and rely on the BinOp-style propagation in nested cases.
		walkExprParams(x.Probe, nil, sch, scopeTable, hint, maxIdx)
		probeType := exprStaticType(x.Probe, sch, scopeTable)
		for _, e := range x.List {
			walkExprParams(e, probeType, sch, scopeTable, hint, maxIdx)
		}
	case *ir.InSubqueryExpr:
		walkExprParams(x.Probe, nil, sch, scopeTable, hint, maxIdx)
		walkParams(x.Plan, sch, scopeTable, hint, maxIdx)
	case *ir.ScalarSubquery:
		walkParams(x.Plan, sch, scopeTable, hint, maxIdx)
	case *ir.Cast:
		// `$1::text` constrains $1 to text. Pass the cast target down
		// as the expected type — exactly the case sqlc-generated NULL
		// inputs use to disambiguate untyped parameters.
		walkExprParams(x.Expr, x.T, sch, scopeTable, hint, maxIdx)
	case *ir.Literal, nil:
		// nothing to record.
	case *ir.ColumnRef:
		// nothing to record.
	}
}

// exprStaticType reports an expression's type without needing exec
// resolution. ColumnRef types come from the catalog using scopeTable.
func exprStaticType(e ir.Expr, sch catalog.Schema, scopeTable string) types.Type {
	switch x := e.(type) {
	case *ir.Literal:
		return x.T
	case *ir.ColumnRef:
		if x.T != nil {
			return x.T
		}
		if scopeTable == "" {
			return nil
		}
		ct, ok := sch.Table(scopeTable)
		if !ok {
			return nil
		}
		for _, c := range ct.Columns {
			if c.Name == x.Name {
				return c.Type
			}
		}
		return nil
	case *ir.BinOp:
		return x.T
	case *ir.UnaryOp:
		return x.T
	case *ir.ParamRef:
		return x.T
	default:
		return nil
	}
}
