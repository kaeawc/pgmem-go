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

// Scan reads every row of the named table.
type Scan struct {
	Table string
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
type Join struct {
	Left  Node
	Right Node
	Cond  Expr
	Type  JoinType
}

func (*Join) node() {}

// SortKey is one ORDER BY clause: an expression and a direction.
type SortKey struct {
	Expr Expr
	Desc bool
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

// --- DDL / DML ---

// CreateTable declares a new table in the catalog and storage.
type CreateTable struct {
	Name    string
	Columns []ColumnDef
}

func (*CreateTable) node() {}

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
}

func (*Delete) node() {}

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

// FuncCall is a builtin function invocation (now(), gen_random_uuid(),
// coalesce(), …). Name is lower-cased by the parser. Type is filled in
// at exec.Build time from the function registry — the parser doesn't
// know what each builtin returns.
type FuncCall struct {
	Name string
	Args []Expr
	T    types.Type
}

func (*FuncCall) expr()              {}
func (f *FuncCall) Type() types.Type { return f.T }

// ScalarSubquery is `(SELECT ...)` used as a value expression. The
// inner plan must produce a single column; producing more than one row
// is a runtime error (PG SQLSTATE 21000). Type is filled in at
// exec.Build time from the inner plan's first column.
type ScalarSubquery struct {
	Plan Node
	T    types.Type
}

func (*ScalarSubquery) expr()              {}
func (s *ScalarSubquery) Type() types.Type { return s.T }

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
