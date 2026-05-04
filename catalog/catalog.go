// Package catalog holds the live schema metadata (tables, columns,
// constraints, indexes) and exposes it to executors.
//
// pg_catalog and information_schema views read from Schema directly;
// see DESIGN.md §4. M1 ships only the bare in-memory Schema; the
// virtual catalog views land with M6.
package catalog

import (
	"fmt"
	"sync"

	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/types"
)

// Column is a single column definition.
type Column struct {
	Name    string
	Type    types.Type
	NotNull bool
	Unique  bool // PRIMARY KEY desugars to NotNull && Unique
	Auto    bool // SERIAL / BIGSERIAL — engine fills missing inserts
	// References, when set, names the parent table+column this column
	// FK-references. Empty Table means no FK on this column.
	References ColumnRef
	// Default, when non-nil, is the column's DEFAULT expression. It's
	// evaluated against an empty row (no column refs) when an INSERT
	// omits this column. Auto columns ignore Default — the SERIAL
	// fill happens first.
	Default ir.Expr
	// Generated, when non-nil, marks the column as `GENERATED ALWAYS
	// AS (expr) STORED`. The exec layer recomputes the column from
	// the row's other columns on every INSERT/UPDATE; users may not
	// supply values for a generated column.
	Generated ir.Expr
}

// ColumnRef names a (table, column) pair on the catalog. Used by
// FOREIGN KEY declarations along with the ON DELETE action.
type ColumnRef struct {
	Table    string
	Column   string
	OnDelete OnDeleteAction
}

// OnDeleteAction mirrors ir.OnDeleteAction (the catalog stays
// ir-package-free so it can be consumed by adapters that don't
// import ir).
type OnDeleteAction int

const (
	OnDeleteRestrict OnDeleteAction = iota
	OnDeleteCascade
	OnDeleteSetNull
)

// Check is one CHECK constraint attached to a table. Real PG names a
// column-level CHECK as `<table>_<col>_check` by default; we follow
// that so error messages match.
type Check struct {
	Name string
	Expr ir.Expr
}

// Table is the metadata for a single table.
type Table struct {
	Name    string
	Columns []Column
	Checks  []Check
}

// Schema is the set of named tables and views known to a server
// instance.
type Schema interface {
	Table(name string) (Table, bool)
	CreateTable(t Table) error
	// DropTable removes the named table. Returns false when the table
	// doesn't exist (the caller decides whether that's an error).
	DropTable(name string) bool
	// Tables returns every table currently in the schema, in
	// insertion order. Used by the FK enforcer to find referencers
	// when a parent row is being deleted.
	Tables() []Table

	// View returns the IR plan registered under the given view name.
	View(name string) (ir.Node, bool)
	CreateView(name string, plan ir.Node) error
	DropView(name string) bool
}

// NewSchema returns an empty in-memory schema.
func NewSchema() Schema {
	return &schema{tables: map[string]Table{}, views: map[string]ir.Node{}}
}

type schema struct {
	mu     sync.RWMutex
	tables map[string]Table
	order  []string // table names in CreateTable order — Tables() uses this
	views  map[string]ir.Node
}

func (s *schema) Table(name string) (Table, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tables[name]
	return t, ok
}

func (s *schema) CreateTable(t Table) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tables[t.Name]; !exists {
		s.order = append(s.order, t.Name)
	}
	s.tables[t.Name] = t
	return nil
}

func (s *schema) DropTable(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tables[name]; !ok {
		return false
	}
	delete(s.tables, name)
	for i, n := range s.order {
		if n == name {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return true
}

func (s *schema) Tables() []Table {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Table, 0, len(s.order))
	for _, name := range s.order {
		out = append(out, s.tables[name])
	}
	return out
}

func (s *schema) View(name string) (ir.Node, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.views[name]
	return v, ok
}

func (s *schema) CreateView(name string, plan ir.Node) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tables[name]; exists {
		return fmt.Errorf("catalog: %q already exists as a table", name)
	}
	s.views[name] = plan
	return nil
}

func (s *schema) DropView(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.views[name]; !ok {
		return false
	}
	delete(s.views, name)
	return true
}
