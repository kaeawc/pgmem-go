// Package catalog holds the live schema metadata (tables, columns,
// constraints, indexes) and exposes it to executors.
//
// pg_catalog and information_schema views read from Schema directly;
// see DESIGN.md §4. M1 ships only the bare in-memory Schema; the
// virtual catalog views land with M6.
package catalog

import (
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
}

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

// Schema is the set of named tables known to a server instance.
type Schema interface {
	Table(name string) (Table, bool)
	CreateTable(t Table) error
}

// NewSchema returns an empty in-memory schema.
func NewSchema() Schema { return &schema{tables: map[string]Table{}} }

type schema struct {
	mu     sync.RWMutex
	tables map[string]Table
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
	s.tables[t.Name] = t
	return nil
}
