// Package ir defines the dialect-neutral logical plan that parsers
// produce and the executor consumes.
//
// Nodes form a tree. There is no optimizer; the shape the parser emits
// is the shape the executor walks.
package ir

import "github.com/kaeawc/pgmem-go/types"

// Node is a logical plan node.
type Node interface{ node() }

// --- Read-side plan nodes ---

// WindowSpec is the OVER (…) clause: optional partition keys and
// optional ordering keys. Frame clauses (ROWS BETWEEN …) aren't
// modelled — row_number / rank / dense_rank don't need them.
type WindowSpec struct {
	PartitionBy []Expr
	OrderBy     []SortKey
}

// WindowCall is one entry in a Window node's Calls slice. Output is
// the synthetic column name the planner uses to thread the result
// back through the surrounding Project.
type WindowCall struct {
	Func   string // lower-case (row_number, rank, dense_rank)
	Args   []Expr
	Spec   WindowSpec
	Output string
}

// Window evaluates a list of window functions against Input. Output
// schema is Input's columns followed by one column per WindowCall in
// Calls order. Each call's value is computed per (PartitionBy,
// OrderBy) group from the spec.
type Window struct {
	Input Node
	Calls []WindowCall
}

func (*Window) node() {}

// Unnest is the table-valued form `unnest(array_expr) [AS alias(col)]`.
// One row per element; the output schema has a single column whose
// name is Alias (default "unnest") and whose type is the array's
// element type.
type Unnest struct {
	Array Expr
	Alias string
}

func (*Unnest) node() {}

// GenerateSeries is the table-valued form `generate_series(start,
// stop[, step]) [AS alias]`. One row per integer in the inclusive
// range [start, stop] stepping by Step (default 1). Step may be
// negative; if Start > Stop with positive Step, the result is
// empty (matches PG). The output schema has a single column named
// after the alias (default "generate_series") whose type matches
// the argument type (int8 by default).
type GenerateSeries struct {
	Start Expr
	Stop  Expr
	Step  Expr // optional; nil means 1
	Alias string
}

func (*GenerateSeries) node() {}

// SubqueryAlias wraps an inline SELECT used in a FROM clause:
//
//	FROM (SELECT ...) sub
//
// It just re-tags each output column with `Alias` as the qualifier so
// `sub.col` resolves; rows pass through untouched.
type SubqueryAlias struct {
	Inner Node
	Alias string
}

func (*SubqueryAlias) node() {}

// Scan reads every row of the named table. Alias, when set, replaces
// the Table value as the qualifier for column references in the scan's
// output schema — i.e. `users u` makes `u.id` the canonical reference.
type Scan struct {
	Table string
	Alias string
}

func (*Scan) node() {}

// Project evaluates Exprs against each row from Input. The OutputNames
// slice is parallel to Exprs and supplies column names for the result
// schema (RowDescription on the wire).
type Project struct {
	Input       Node
	Exprs       []Expr
	OutputNames []string
}

func (*Project) node() {}

// Values produces a fixed set of literal rows. A single empty row
// (Rows = [][]Expr{{}}) is the standard "no FROM clause" producer.
type Values struct {
	Rows [][]Expr
}

func (*Values) node() {}

// Filter keeps only rows for which Cond evaluates to true.
type Filter struct {
	Input Node
	Cond  Expr
}

func (*Filter) node() {}

// JoinType enumerates the join flavors. M4 ships with only Inner;
// Left and Cross arrive in follow-up pieces but the IR is shaped to
// receive them without restructuring.
type JoinType int

const (
	JoinInner JoinType = iota
	JoinLeft
	JoinCross
)

// Join produces concatenated rows from Left and Right that satisfy
// Cond. For an INNER JOIN the right rows that match are emitted; for
// LEFT they are augmented with NULLs when no match exists. CROSS
// ignores Cond.
//
// Lateral, when true, marks the join as a LATERAL join: the Right
// plan can reference Left's columns (typical shape: derived-table
// subquery on the right). The executor rebuilds the right operator
// per left row with the outer row threaded through env.
type Join struct {
	Left    Node
	Right   Node
	Cond    Expr
	Type    JoinType
	Lateral bool
}

func (*Join) node() {}

// NullsOrder controls where NULLs sort relative to non-NULLs in an
// ORDER BY clause. NullsDefault (the zero value) follows real PG: ASC
// → NULLS LAST, DESC → NULLS FIRST.
type NullsOrder int

const (
	NullsDefault NullsOrder = iota
	NullsFirst
	NullsLast
)

// SortKey is one ORDER BY clause: an expression, a direction, and an
// optional explicit NULLS placement.
type SortKey struct {
	Expr  Expr
	Desc  bool
	Nulls NullsOrder
}

// Sort orders Input's rows by the SortKeys in lexicographic order.
type Sort struct {
	Input Node
	Keys  []SortKey
}

func (*Sort) node() {}

// Limit slices Input. A nil Count means "unlimited"; Offset defaults
// to zero. Both expressions must evaluate to a constant int at exec
// build time (parameters are fine).
type Limit struct {
	Input  Node
	Count  Expr
	Offset Expr
}

func (*Limit) node() {}

// Union concatenates Left's and Right's rows in order. With All true
// duplicates pass through; with All false the output is deduplicated
// (UNION = UNION DISTINCT). Both sides must produce the same number
// of columns; column names come from Left.
type Union struct {
	Left  Node
	Right Node
	All   bool
}

func (*Union) node() {}

// Distinct keeps only one row per unique tuple of Input's columns.
// Equivalent to wrapping a SELECT DISTINCT in a hash-set deduplicator.
// Output schema matches Input.
//
// On, when non-empty, restricts uniqueness to the values of the
// listed expressions instead of all output columns — that's the
// `SELECT DISTINCT ON (cols) …` form. The first row per distinct
// (cols) tuple is kept; ORDER BY decides which row that is.
type Distinct struct {
	Input Node
	On    []Expr
}

func (*Distinct) node() {}

// AggregateCall is one entry in an Aggregate node's Calls slice.
//
//	Func   — lower-case name (count, sum, min, max, avg, string_agg).
//	Args   — the argument expressions. Empty for COUNT(*); single
//	         entry for unary aggregates; two entries for two-arg
//	         aggregates like string_agg(expr, sep).
//	Output — the result column name in the Aggregate's output schema.
type AggregateCall struct {
	Func     string
	Args     []Expr
	Output   string
	Distinct bool // true for `agg(DISTINCT expr)` — dedupe before accumulating
	Filter   Expr // when non-nil, only rows where Filter→true are accepted
}

// Aggregate computes input aggregation. With GroupBy empty (the
// "scalar aggregate" shape), Aggregate drains Input and emits one
// row whose columns are the Calls' results — and emits a row even
// when the input is empty (COUNT → 0, MIN/MAX/SUM/AVG → NULL).
//
// With GroupBy non-empty, Aggregate hashes input rows by the GroupBy
// expressions' values and emits one row per distinct group. Output
// schema is [GroupBy[0], …, GroupBy[N-1], Calls[0], …, Calls[M-1]] —
// the Project that wraps the Aggregate uses that ordering to rewire
// the user's SELECT list.
//
// GROUP BY expressions are restricted to bare column references in
// this slice; expression-based grouping (`GROUP BY a + b`) is a
// follow-up. Storing them as Expr keeps the IR ready for it.
type Aggregate struct {
	Input   Node
	Calls   []AggregateCall
	GroupBy []Expr
}

func (*Aggregate) node() {}

// --- DDL / DML ---

// CreateTable declares a new table in the catalog and storage.
type CreateTable struct {
	Name    string
	Columns []ColumnDef
}

func (*CreateTable) node() {}

// AlterTableAction tags one of the schema-mutation forms our ALTER
// TABLE accepts: add a column, drop a column, rename a column. PG
// allows comma-separated mixes; we model each as its own statement
// today.
type AlterTableAction int

const (
	AlterTableAddColumn AlterTableAction = iota
	AlterTableDropColumn
	AlterTableRenameColumn
	AlterTableSetNotNull
	AlterTableDropNotNull
)

// AlterTable mutates a table's schema in place. AddCol carries the
// new column when Action is AlterTableAddColumn; DropName /
// IfExistsCol applies to AlterTableDropColumn; RenameOldName /
// RenameNewName apply to AlterTableRenameColumn.
type AlterTable struct {
	Table       string
	Action      AlterTableAction
	AddCol      ColumnDef
	DropName    string
	IfExistsCol bool
	RenameOld   string
	RenameNew   string
	// AlterCol is the target column name for ALTER COLUMN actions
	// (AlterTableSetNotNull, AlterTableDropNotNull).
	AlterCol string
}

func (*AlterTable) node() {}

// CreateIndex is a no-op DDL — pgmem-go has no real index machinery,
// so CREATE INDEX is accepted purely for migration-tool / sqlc-init
// compatibility. The Name / Table / Concurrently / IfNotExists
// fields are kept so a future indexed-scan piece can grow the
// behaviour without an IR rev.
type CreateIndex struct {
	Name         string
	Table        string
	IfNotExists  bool
	Concurrently bool
}

func (*CreateIndex) node() {}

// DropIndex is the partner no-op for CREATE INDEX. IfExists keeps
// the cleanup path quiet on a missing index.
type DropIndex struct {
	Name     string
	IfExists bool
}

func (*DropIndex) node() {}

// CreateView registers a named view backed by an IR plan. The view
// shows up in the catalog like a read-only table; SELECT against it
// inlines the plan.
type CreateView struct {
	Name string
	Plan Node
}

func (*CreateView) node() {}

// DropView removes a view from the catalog. IfExists makes a missing
// view a no-op rather than an error.
type DropView struct {
	Name     string
	IfExists bool
}

func (*DropView) node() {}

// Truncate empties the listed tables. Real PG also resets identity
// sequences when RESTART IDENTITY is given; we accept the option as a
// no-op for now since we don't yet model sequences as first-class
// catalog objects.
type Truncate struct {
	Tables          []string
	RestartIdentity bool
}

func (*Truncate) node() {}

// DropTable removes a table from the catalog and the storage engine.
// IfExists makes a missing table a no-op rather than an error.
type DropTable struct {
	Name     string
	IfExists bool
}

func (*DropTable) node() {}

// ColumnDef is one column in a CREATE TABLE statement.
type ColumnDef struct {
	Name    string
	Type    types.Type
	NotNull bool
	Unique  bool // PRIMARY KEY sets both NotNull and Unique
	// Auto means the column is filled by the engine when an INSERT
	// doesn't supply a value (SERIAL / BIGSERIAL). Real PG implements
	// this via a separate sequence object and a DEFAULT nextval(...);
	// we condense it to a single boolean since we don't model
	// sequences as first-class catalog objects yet.
	Auto bool
	// Check is the optional CHECK (expr) constraint attached to the
	// column. Real PG allows the expression to reference *other* columns
	// of the same row; we follow that, with the executor resolving
	// column refs against the table schema at INSERT time.
	Check Expr
	// References, when non-nil, declares a single-column FOREIGN KEY:
	// `REFERENCES <table>(<column>)`. ON DELETE behavior follows in a
	// later slice — for now the constraint is implicitly RESTRICT.
	References *ColumnRefSpec
	// Default, when non-nil, is the column's DEFAULT expression. The
	// executor resolves it against an empty schema and evaluates it
	// per-INSERT for any row that omitted the column.
	Default Expr
}

// ColumnRefSpec is the (table, column) pair a FOREIGN KEY references,
// plus the ON DELETE action. We use a struct rather than embedding the
// catalog type so the IR stays catalog-package-free.
type ColumnRefSpec struct {
	Table    string
	Column   string
	OnDelete OnDeleteAction
}

// OnDeleteAction enumerates the FK ON DELETE behaviours we model. The
// zero value is RESTRICT, which matches PG's default when the clause
// is omitted.
type OnDeleteAction int

const (
	OnDeleteRestrict OnDeleteAction = iota // default; reject delete with 23503
	OnDeleteCascade                        // delete the dependent rows too
	OnDeleteSetNull                        // null out the FK column on dependents
)

// Assignment is one `column = expr` clause in an UPDATE's SET list.
type Assignment struct {
	Column string
	Expr   Expr
}

// Update modifies matching rows of the named table. Each Assignment's
// expression is evaluated against the row's pre-update values
// (matching real PG: assignments don't see each other's effects within
// the same UPDATE). RETURNING expressions, in contrast, see the
// post-update row.
type Update struct {
	Table          string
	Assignments    []Assignment
	Where          Expr
	Returning      []Expr
	ReturningNames []string
	// From, when non-nil, joins additional tables into the update
	// scope: `UPDATE t SET … FROM other WHERE …`. SET expressions and
	// WHERE see both target columns and From's output schema.
	From Node
}

func (*Update) node() {}

// Delete removes rows from the named table that match Where. Where may
// be nil, in which case every row is deleted. Returning, when set,
// emits one row per deleted row, evaluated against the row's pre-
// delete values (matching real PG).
type Delete struct {
	Table          string
	Where          Expr
	Returning      []Expr
	ReturningNames []string
	// Using mirrors UPDATE's From: extra tables visible to WHERE for
	// `DELETE FROM t USING other WHERE …`.
	Using Node
}

func (*Delete) node() {}

// OnConflict carries the policy for INSERT ... ON CONFLICT. Columns
// names the conflict-target columns. In real PG these must match a
// unique constraint or index — we don't enforce that yet, so the
// policy applies whenever the named columns alone match an existing
// row. DoNothing and DoUpdate are mutually exclusive; exactly one is
// set when OnConflict itself is non-nil.
type OnConflict struct {
	Columns   []string
	DoNothing bool
	// DoUpdate, when non-empty, rewrites the conflicting row's columns
	// using these assignments. Each assignment's expression sees both
	// the existing row's columns and the proposed row's columns as a
	// catalog-ordered concatenation — qualified `excluded.col` refers
	// to the proposed-but-blocked value, bare `col` refers to the
	// existing value (matching real PG).
	DoUpdate []Assignment
}

// Insert appends rows to the named table. Columns names the target
// columns in order; if empty, every column of the table is targeted in
// catalog order. Rows is parallel to Columns.
//
// If Returning is non-empty, the operator produces one output row per
// inserted row, with each Expr evaluated against the freshly-inserted
// row (so column refs see post-INSERT values). ReturningNames is
// parallel to Returning and supplies the result schema's column names.
type Insert struct {
	Table          string
	Columns        []string
	Rows           [][]Expr
	Returning      []Expr
	ReturningNames []string
	OnConflict     *OnConflict
	// Source, when non-nil, replaces Rows: each row produced by the
	// inner plan supplies one INSERT tuple. Column count of the inner
	// plan must match Columns (or the table's full column list when
	// Columns is empty). Rows and Source are mutually exclusive.
	Source Node
	// DefaultValues is set for `INSERT INTO t DEFAULT VALUES`. The
	// runtime emits a single row with every column either auto-filled
	// (SERIAL) or NULL — NOT NULL columns without an auto fill cause
	// the standard 23502 error.
	DefaultValues bool
}

func (*Insert) node() {}

// --- Expressions ---

// Expr is a scalar expression evaluated per input row.
type Expr interface {
	expr()
	// Type reports the expression's static type. Some expression kinds
	// (ColumnRef, ParamRef) have their type filled in during planning,
	// not parsing.
	Type() types.Type
}

// Literal is a constant value of a known type.
type Literal struct {
	Value any
	T     types.Type
}

func (*Literal) expr()              {}
func (l *Literal) Type() types.Type { return l.T }

// ColumnRef refers to a column in the input row by zero-based index.
// The static type comes from the input operator's schema and is
// resolved at exec.Build time.
type ColumnRef struct {
	// Qualifier is the table-name prefix (`users` in `users.id`).
	// Empty when the SQL source wrote a bare identifier, in which case
	// resolution falls back to a single-match name lookup against the
	// input schema and errors on ambiguity.
	Qualifier string
	// Name is the unresolved column name from the SQL source. It is set
	// by the parser; exec.Build uses it (and the input schema) to fill
	// in Index and T.
	Name  string
	Index int
	T     types.Type
	// Outer is true when this column reference resolves against the
	// surrounding query's row rather than the inner operator's input.
	// Used by correlated EXISTS / subqueries so the per-row evaluator
	// reads the outer-scope value via env.OuterRow[Index].
	Outer bool
}

func (*ColumnRef) expr()              {}
func (c *ColumnRef) Type() types.Type { return c.T }

// ParamRef is a $N placeholder. Index is zero-based ($1 → 0). The type
// is filled in either from the Parse OID or, lacking that, from the
// Bind format + value.
type ParamRef struct {
	Index int
	T     types.Type
}

func (*ParamRef) expr()              {}
func (p *ParamRef) Type() types.Type { return p.T }

// BinOp is a binary operator on two expressions. Op is the SQL token
// ("=", "<", "and", ...) lower-cased.
type BinOp struct {
	Op    string
	Left  Expr
	Right Expr
	T     types.Type
}

func (*BinOp) expr()              {}
func (b *BinOp) Type() types.Type { return b.T }

// UnaryOp covers NOT and (eventually) unary minus.
type UnaryOp struct {
	Op   string
	Expr Expr
	T    types.Type
}

func (*UnaryOp) expr()              {}
func (u *UnaryOp) Type() types.Type { return u.T }

// StarRef is the `*` placeholder in a SELECT list. Planning expands
// it to the full set of input-schema columns (`expand` in
// exec/project.go) — it never reaches evaluation time. Qualifier,
// when set, restricts expansion to columns whose schema entry has a
// matching qualifier — that's the `t.*` form.
type StarRef struct {
	Qualifier string
}

func (*StarRef) expr()            {}
func (*StarRef) Type() types.Type { return nil }

// Cast is `expr::type` — runtime conversion to a named type. The exec
// layer's converter table covers a small subset of PG's implicit-cast
// lattice (DESIGN.md §5: "implement only the casts sqlc-generated
// code emits"). Unsupported casts surface as exec errors.
type Cast struct {
	Expr Expr
	T    types.Type
}

func (*Cast) expr()              {}
func (c *Cast) Type() types.Type { return c.T }

// FuncCall is a builtin function invocation (now(), gen_random_uuid(),
// coalesce(), …). Name is lower-cased by the parser. Type is filled in
// at exec.Build time from the function registry — the parser doesn't
// know what each builtin returns.
//
// Star is the count(*) marker. Aggregate-aware planning checks it to
// turn a FuncCall into an AggregateCall. For non-aggregate builtins
// Star is always false and the executor treats Star as an arity error.
type FuncCall struct {
	Name     string
	Args     []Expr
	T        types.Type
	Star     bool
	Distinct bool // true for `agg(DISTINCT expr)`; ignored on non-aggregate calls
	// Filter, when non-nil, restricts which input rows the aggregate
	// observes (`agg(expr) FILTER (WHERE cond)`). Ignored on non-
	// aggregate calls.
	Filter Expr
	// Window, when non-nil, marks this call as a window function with
	// the given OVER spec. Window calls are extracted from the
	// SELECT list during planning and replaced with synthetic column
	// references; the runtime never sees a FuncCall whose Window is
	// still attached.
	Window *WindowSpec
}

func (*FuncCall) expr()              {}
func (f *FuncCall) Type() types.Type { return f.T }

// ScalarSubquery is `(SELECT ...)` used as a value expression. The
// inner plan must produce a single column; producing more than one row
// is a runtime error (PG SQLSTATE 21000). Type is filled in at
// exec.Build time from the inner plan's first column.
//
// OuterSchema, when non-nil, lets the inner plan reference outer-
// scope columns — same pattern as ExistsExpr for correlated EXISTS.
type ScalarSubquery struct {
	Plan        Node
	T           types.Type
	OuterSchema []OuterField
}

func (*ScalarSubquery) expr()              {}
func (s *ScalarSubquery) Type() types.Type { return s.T }

// OuterField describes a single column from the surrounding query's
// scope. Captured on a subquery expression at resolve time so the
// inner operator's resolveColumnRef can fall back to the outer
// schema for correlated references.
type OuterField struct {
	Qualifier string
	Name      string
	T         types.Type
}

// ExistsExpr is `EXISTS (SELECT ...)` — true when the inner plan
// produces at least one row, false otherwise. OuterSchema, when
// non-nil, lets the inner plan's expressions reference outer-scope
// columns (`r.id` in `WHERE EXISTS (SELECT … WHERE s.room_id =
// r.id)`); ColumnRefs that resolve against it are tagged Outer.
type ExistsExpr struct {
	Plan        Node
	OuterSchema []OuterField
}

func (*ExistsExpr) expr()            {}
func (*ExistsExpr) Type() types.Type { return boolType }

// CaseWhen is one branch of a Case expression.
type CaseWhen struct {
	// Match is the WHEN expression. For a "searched" CASE (no operand)
	// it's a bool predicate; for a "simple" CASE it's compared to the
	// outer Operand for equality.
	Match Expr
	// Result is the THEN expression evaluated when this branch fires.
	Result Expr
}

// Case is `CASE [operand] WHEN ... THEN ... [ELSE ...] END`. Operand
// is nil for the searched form. Else may be nil — when no branch
// fires and there is no ELSE, the result is NULL.
type Case struct {
	Operand Expr
	Whens   []CaseWhen
	Else    Expr
	T       types.Type
}

func (*Case) expr()              {}
func (c *Case) Type() types.Type { return c.T }

// AnyExpr is `probe op ANY (array)`: true iff any element of the
// array satisfies `probe op element`. We currently support op `=`
// (the most common form, and what sqlc emits for list parameters).
type AnyExpr struct {
	Probe Expr
	Op    string
	Array Expr
}

func (*AnyExpr) expr()            {}
func (*AnyExpr) Type() types.Type { return boolType }

// InListExpr is `probe IN (val1, val2, ...)`. Result is always bool.
type InListExpr struct {
	Probe Expr
	List  []Expr
}

func (*InListExpr) expr()            {}
func (*InListExpr) Type() types.Type { return boolType }

// InSubqueryExpr is `probe IN (SELECT ...)`. The inner plan must
// produce a single column. Result is always bool.
type InSubqueryExpr struct {
	Probe Expr
	Plan  Node
}

func (*InSubqueryExpr) expr()            {}
func (*InSubqueryExpr) Type() types.Type { return boolType }

// boolType is a small indirection so we don't take a `types.Bool`
// dependency at package init time (avoids any future ordering issues
// when the dialect package re-registers types).
var boolType = types.Bool
