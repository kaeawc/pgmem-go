package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kaeawc/pgmem-go/storage"
)

// TestCommit_Conflict_TwoConcurrentWrites: two txns both snapshot a
// table at version N and both write; the second to commit gets
// SerializationError.
func TestCommit_Conflict_TwoConcurrentWrites(t *testing.T) {
	eng := storage.NewEngine()
	eng.CreateTable("items", 1)

	txnA, _ := eng.Begin(context.Background())
	tA, _ := txnA.Table("items")
	tA.Insert(storage.Row{int32(1)})

	txnB, _ := eng.Begin(context.Background())
	tB, _ := txnB.Table("items")
	tB.Insert(storage.Row{int32(2)})

	if err := txnA.Commit(); err != nil {
		t.Fatalf("Commit A: %v", err)
	}
	err := txnB.Commit()
	var sErr *storage.SerializationError
	if !errors.As(err, &sErr) {
		t.Fatalf("Commit B: got %v, want SerializationError", err)
	}
	if sErr.Table != "items" {
		t.Errorf("Table: got %q, want items", sErr.Table)
	}

	// Only A's row landed; B's is gone.
	canon, _ := eng.Table("items")
	rows := canon.Rows()
	if len(rows) != 1 || rows[0][0] != int32(1) {
		t.Errorf("canonical: got %v, want [{1}]", rows)
	}
}

// TestCommit_NoConflict_OneSideReadOnly: a writing tx and a reading
// tx don't conflict at commit. The reader's snapshot view is
// preserved, the writer commits clean.
func TestCommit_NoConflict_OneSideReadOnly(t *testing.T) {
	eng := storage.NewEngine()
	eng.CreateTable("items", 1)
	canon, _ := eng.Table("items")
	canon.Insert(storage.Row{int32(0)})

	reader, _ := eng.Begin(context.Background())
	rt, _ := reader.Table("items")
	if got := len(rt.Rows()); got != 1 {
		t.Errorf("reader rows: got %d, want 1", got)
	}

	writer, _ := eng.Begin(context.Background())
	wt, _ := writer.Table("items")
	wt.Insert(storage.Row{int32(1)})
	if err := writer.Commit(); err != nil {
		t.Fatalf("writer commit: %v", err)
	}

	// Reader's snapshot still shows the old state.
	if got := len(rt.Rows()); got != 1 {
		t.Errorf("reader after writer commit: got %d, want 1", got)
	}
	// Reader commits clean (no dirty tables).
	if err := reader.Commit(); err != nil {
		t.Errorf("reader commit: got %v, want nil", err)
	}
}

// TestCommit_NoConflict_DisjointTables: two txns writing different
// tables don't conflict.
func TestCommit_NoConflict_DisjointTables(t *testing.T) {
	eng := storage.NewEngine()
	eng.CreateTable("a", 1)
	eng.CreateTable("b", 1)

	txnA, _ := eng.Begin(context.Background())
	tA, _ := txnA.Table("a")
	tA.Insert(storage.Row{int32(1)})

	txnB, _ := eng.Begin(context.Background())
	tB, _ := txnB.Table("b")
	tB.Insert(storage.Row{int32(2)})

	if err := txnA.Commit(); err != nil {
		t.Fatalf("Commit A: %v", err)
	}
	if err := txnB.Commit(); err != nil {
		t.Errorf("Commit B: got %v, want nil", err)
	}
}

// TestCommit_ConflictDetected_AcrossDelete: same conflict semantics
// hold when one side deletes via Mutate.
func TestCommit_ConflictDetected_AcrossDelete(t *testing.T) {
	eng := storage.NewEngine()
	eng.CreateTable("items", 1)
	canon, _ := eng.Table("items")
	canon.Insert(storage.Row{int32(1)})
	canon.Insert(storage.Row{int32(2)})

	txnA, _ := eng.Begin(context.Background())
	tA, _ := txnA.Table("items")
	tA.Mutate(func(rows []storage.Row) []storage.Row { return rows[:1] }) // remove row 2

	txnB, _ := eng.Begin(context.Background())
	tB, _ := txnB.Table("items")
	tB.Insert(storage.Row{int32(3)})

	if err := txnA.Commit(); err != nil {
		t.Fatalf("Commit A: %v", err)
	}
	err := txnB.Commit()
	var sErr *storage.SerializationError
	if !errors.As(err, &sErr) {
		t.Errorf("Commit B: got %v, want SerializationError", err)
	}
}
