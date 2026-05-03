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
	// Check is the optional CHECK (expr) constraint attached to the
	// column. Real PG allows the expression to reference *other* columns
	// of the same row; we follow that, with the executor resolving
	// column refs against the table schema at INSERT time.
	Check Expr
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
