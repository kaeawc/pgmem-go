// Package storage holds the in-memory tables, indexes, and the
// transaction overlay that gives us snapshot isolation without a
// full MVCC implementation.
//
// See DESIGN.md §3 for the storage model. Each transaction takes a
// per-table snapshot on first access; writes go into that snapshot
// only; Commit replaces the engine's canonical rows under the table
// write lock.
//
// What we deliberately don't do (yet):
//   - Concurrent-tx conflict detection. Two simultaneous transactions
//     that touch the same table will both commit — last writer wins.
//     Real PG SI rejects the second commit with a serialization error;
//     a follow-up piece adds that.
//   - Per-row version chains. Snapshots are whole-table copies; fine
//     at the M3 row counts test suites use.
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
	// DropTable removes a table; returns false when the name didn't
	// exist. Concurrent in-flight transactions that already snapshotted
	// the table keep their snapshot — the engine drop is independent of
	// per-tx state.
	DropTable(name string) bool
}

// Txn is a transaction handle. The executor only ever sees Tables
// through Txn — the canonical engine state is reachable only via
// Begin.
type Txn interface {
	Commit() error
	Rollback() error
	Table(name string) (Table, bool)
	// Savepoint marks a sub-transaction boundary. Subsequent
	// RollbackTo(name) restores the txn's snapshot state to this point
	// without ending the txn. ReleaseSavepoint(name) discards the
	// savepoint (and any inside it) without rolling back. Names are
	// case-sensitive; reusing one shadows the older entry per PG.
	Savepoint(name string) error
	RollbackToSavepoint(name string) error
	ReleaseSavepoint(name string) error
}

// SavepointError is returned by RollbackToSavepoint / ReleaseSavepoint
// when the named savepoint isn't on the stack. The wire layer maps it
// to SQLSTATE 3B001 ("invalid savepoint specification") to match PG.
type SavepointError struct{ Name string }

func (e *SavepointError) Error() string { return "savepoint " + e.Name + " does not exist" }

// Table is an iterable, mutable collection of rows.
type Table interface {
	Name() string
	// Rows returns a snapshot copy of the current rows. The copy is safe
	// for the executor to walk without holding any storage locks.
	Rows() []Row
	Insert(r Row)
	// Mutate atomically rewrites the table's row set. mutator receives a
	// fresh copy of the current rows and returns the desired replacement.
	// Inside a Txn this only mutates the per-tx snapshot; Commit applies.
	Mutate(mutator func([]Row) []Row)
	// NextAuto advances and returns the per-column SERIAL / BIGSERIAL
	// counter. The counter lives on the canonical table, not on the
	// txn snapshot — gaps from rolled-back transactions are intentional
	// and match real PG sequence behaviour.
	NextAuto(colIdx int) int64
}

// NewEngine returns an empty in-memory engine.
func NewEngine() Engine { return &engine{tables: map[string]*table{}} }

type engine struct {
	mu     sync.RWMutex
	tables map[string]*table
}

func (e *engine) Begin(_ context.Context) (Txn, error) {
	return &txn{e: e, snapshots: map[string]*txnTable{}}, nil
}

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

func (e *engine) DropTable(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.tables[name]; !ok {
		return false
	}
	delete(e.tables, name)
	return true
}

// --- canonical (committed) table ---

type table struct {
	mu       sync.RWMutex
	name     string
	ncols    int
	rows     []Row
	autoMu   sync.Mutex // guards the per-column SERIAL counters
	autoNext map[int]int64
}

func (t *table) Name() string { return t.name }

func (t *table) Insert(r Row) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rows = append(t.rows, append(Row(nil), r...))
}

func (t *table) Rows() []Row { return copyRows(t.lockedRows()) }

func (t *table) lockedRows() []Row {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.rows
}

func (t *table) Mutate(mutator func([]Row) []Row) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rows = mutator(copyRows(t.rows))
}

// replaceLocked is used by Commit to overwrite canonical state from a
// txn snapshot. The caller holds t.mu.
func (t *table) replaceLocked(rows []Row) {
	t.rows = rows
}

// NextAuto advances and returns the SERIAL counter for colIdx. First
// call per column returns 1. Concurrent callers see distinct values.
func (t *table) NextAuto(colIdx int) int64 {
	t.autoMu.Lock()
	defer t.autoMu.Unlock()
	if t.autoNext == nil {
		t.autoNext = map[int]int64{}
	}
	t.autoNext[colIdx]++
	return t.autoNext[colIdx]
}

// --- transaction & per-tx snapshots ---

type txn struct {
	e          *engine
	mu         sync.Mutex // guards snapshots, savepoints and closed
	snapshots  map[string]*txnTable
	savepoints []savepointFrame
	closed     bool
}

// savepointFrame is the per-table snapshot captured at SAVEPOINT time.
// We deep-copy each touched table's rows so RollbackTo can restore
// them. New tables touched after the savepoint disappear from the
// snapshots map on RollbackTo (matching PG: post-savepoint work is
// undone, including newly-touched tables).
type savepointFrame struct {
	name   string
	tables map[string][]Row
}

func (t *txn) Table(name string) (Table, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, false
	}
	if cached, ok := t.snapshots[name]; ok {
		return cached, true
	}
	t.e.mu.RLock()
	canonical, ok := t.e.tables[name]
	t.e.mu.RUnlock()
	if !ok {
		return nil, false
	}
	wrap := &txnTable{name: name, rows: copyRows(canonical.lockedRows()), canonical: canonical}
	t.snapshots[name] = wrap
	return wrap, true
}

// Commit applies dirty per-tx snapshots back to the engine. Tables we
// only read (dirty == false) are skipped. We acquire each canonical
// table's write lock in deterministic name order to avoid the trivial
// deadlock of two txns committing two tables in opposite orders.
func (t *txn) Commit() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	dirty := make([]*txnTable, 0, len(t.snapshots))
	for _, snap := range t.snapshots {
		if snap.dirty {
			dirty = append(dirty, snap)
		}
	}
	t.mu.Unlock()
	sortByName(dirty)
	for _, snap := range dirty {
		t.e.mu.RLock()
		canonical := t.e.tables[snap.name]
		t.e.mu.RUnlock()
		if canonical == nil {
			continue // table was dropped between snapshot and commit
		}
		canonical.mu.Lock()
		canonical.replaceLocked(copyRows(snap.rows))
		canonical.mu.Unlock()
	}
	return nil
}

// Rollback drops the snapshot set. No engine state is touched because
// no engine state was ever modified.
func (t *txn) Rollback() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	t.snapshots = nil
	t.savepoints = nil
	return nil
}

// Savepoint captures the current per-table snapshot state under name.
// Reusing a name shadows the previous entry — RollbackTo(name) finds
// the topmost match — matching PG's semantics for SAVEPOINT inside
// SAVEPOINT.
func (t *txn) Savepoint(name string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	frame := savepointFrame{name: name, tables: make(map[string][]Row, len(t.snapshots))}
	for tbl, snap := range t.snapshots {
		snap.mu.Lock()
		frame.tables[tbl] = copyRows(snap.rows)
		snap.mu.Unlock()
	}
	t.savepoints = append(t.savepoints, frame)
	return nil
}

// RollbackToSavepoint restores the per-tx state to the most recent
// matching savepoint. Newer savepoints are discarded; the named
// savepoint stays on the stack so a subsequent RollbackTo or RELEASE
// can target it. Tables touched only after the savepoint are dropped
// from the snapshot map.
func (t *txn) RollbackToSavepoint(name string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	idx := findSavepoint(t.savepoints, name)
	if idx < 0 {
		return &SavepointError{Name: name}
	}
	frame := t.savepoints[idx]
	// Restore: replace snapshots with deep copies from the frame.
	// We re-link each restored snapshot to its canonical so subsequent
	// NextAuto calls keep advancing from the engine's counter.
	t.snapshots = make(map[string]*txnTable, len(frame.tables))
	for tbl, rows := range frame.tables {
		t.e.mu.RLock()
		canonical := t.e.tables[tbl]
		t.e.mu.RUnlock()
		t.snapshots[tbl] = &txnTable{name: tbl, rows: copyRows(rows), dirty: true, canonical: canonical}
	}
	// Drop savepoints created after the target.
	t.savepoints = t.savepoints[:idx+1]
	return nil
}

// ReleaseSavepoint pops the named savepoint and any newer ones. The
// per-tx state itself is unchanged; subsequent RollbackTo can no
// longer target a released savepoint.
func (t *txn) ReleaseSavepoint(name string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	idx := findSavepoint(t.savepoints, name)
	if idx < 0 {
		return &SavepointError{Name: name}
	}
	t.savepoints = t.savepoints[:idx]
	return nil
}

// findSavepoint scans from the top of the stack — PG behavior on
// duplicate names is "match the most recent".
func findSavepoint(stack []savepointFrame, name string) int {
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].name == name {
			return i
		}
	}
	return -1
}

// --- txn-scoped table wrapper ---

type txnTable struct {
	mu        sync.Mutex // guards rows / dirty (a txn is single-conn but Mutate may capture)
	name      string
	rows      []Row
	dirty     bool
	canonical *table // for NextAuto pass-through
}

func (tt *txnTable) Name() string { return tt.name }

func (tt *txnTable) Insert(r Row) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	tt.rows = append(tt.rows, append(Row(nil), r...))
	tt.dirty = true
}

func (tt *txnTable) Rows() []Row {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	return copyRows(tt.rows)
}

func (tt *txnTable) Mutate(mutator func([]Row) []Row) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	tt.rows = mutator(copyRows(tt.rows))
	tt.dirty = true
}

// NextAuto delegates to the canonical table so SERIAL counters don't
// reset per transaction. A rolled-back txn's allocated values are
// gaps — same as PG's nextval.
func (tt *txnTable) NextAuto(colIdx int) int64 {
	return tt.canonical.NextAuto(colIdx)
}

// --- helpers ---

func copyRows(in []Row) []Row {
	out := make([]Row, len(in))
	for i, r := range in {
		out[i] = append(Row(nil), r...)
	}
	return out
}

func sortByName(ts []*txnTable) {
	// Tiny insertion sort — typical txns touch a handful of tables.
	for i := 1; i < len(ts); i++ {
		for j := i; j > 0 && ts[j-1].name > ts[j].name; j-- {
			ts[j-1], ts[j] = ts[j], ts[j-1]
		}
	}
}
