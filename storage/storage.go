// Package storage holds the in-memory tables, indexes, and the
// transaction overlay that gives us snapshot isolation without a
// full MVCC implementation.
//
// See DESIGN.md §3 for the storage model. M1 ships only the trivial
// shape (rows behind RWMutex, no transactions); the snapshot/COW
// overlay lands with M3.
package storage

import (
	"context"
	"sync"
)

// Row is a single tuple of column values. The slice index matches the
// column index in the table's catalog definition.
type Row []any

// Engine owns the set of tables in a single pgmem server instance.
type Engine interface {
	Begin(ctx context.Context) (Txn, error)
	Table(name string) (Table, bool)
	CreateTable(name string, columnCount int) Table
}

// Txn is a transaction handle. The executor never sees a snapshot
// directly — it goes through Txn. M1's implementation is a no-op
// passthrough; real snapshot isolation arrives in M3.
type Txn interface {
	Commit() error
	Rollback() error
	Table(name string) (Table, bool)
}

// Table is an iterable, mutable collection of rows.
type Table interface {
	Name() string
	// Rows returns a snapshot copy of the current rows. The copy is safe
	// for the executor to walk without holding any storage locks.
	Rows() []Row
	Insert(r Row)
}

// NewEngine returns an empty in-memory engine.
func NewEngine() Engine { return &engine{tables: map[string]*table{}} }

type engine struct {
	mu     sync.RWMutex
	tables map[string]*table
}

func (e *engine) Begin(_ context.Context) (Txn, error) { return &txn{e: e}, nil }

func (e *engine) Table(name string) (Table, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	t, ok := e.tables[name]
	if !ok {
		return nil, false
	}
	return t, true
}

func (e *engine) CreateTable(name string, columnCount int) Table {
	e.mu.Lock()
	defer e.mu.Unlock()
	t := &table{name: name, ncols: columnCount}
	e.tables[name] = t
	return t
}

type txn struct{ e *engine }

func (t *txn) Commit() error                   { return nil }
func (t *txn) Rollback() error                 { return nil }
func (t *txn) Table(name string) (Table, bool) { return t.e.Table(name) }

type table struct {
	mu    sync.RWMutex
	name  string
	ncols int
	rows  []Row
}

func (t *table) Name() string { return t.name }

func (t *table) Insert(r Row) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rows = append(t.rows, append(Row(nil), r...))
}

func (t *table) Rows() []Row {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]Row, len(t.rows))
	for i, r := range t.rows {
		out[i] = append(Row(nil), r...)
	}
	return out
}
