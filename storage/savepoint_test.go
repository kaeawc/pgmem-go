package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kaeawc/pgmem-go/storage"
)

func freshTxn(t *testing.T) (storage.Engine, storage.Txn) {
	t.Helper()
	eng := storage.NewEngine()
	eng.CreateTable("items", 1)
	c, _ := eng.Table("items")
	c.Insert(storage.Row{int32(1)})
	c.Insert(storage.Row{int32(2)})
	tx, _ := eng.Begin(context.Background())
	return eng, tx
}

func count(tt storage.Table) int { return len(tt.Rows()) }

// TestSavepoint_RollbackUndoesOnlyPostSavepointWork: pre-savepoint
// inserts survive, post-savepoint inserts get reverted.
func TestSavepoint_RollbackUndoesOnlyPostSavepointWork(t *testing.T) {
	_, tx := freshTxn(t)
	defer tx.Rollback()

	tt, _ := tx.Table("items")
	tt.Insert(storage.Row{int32(3)}) // pre-savepoint

	if err := tx.Savepoint("sp1"); err != nil {
		t.Fatalf("Savepoint: %v", err)
	}
	tt.Insert(storage.Row{int32(4)})
	tt.Insert(storage.Row{int32(5)})
	if got := count(tt); got != 5 {
		t.Errorf("pre-rollback count: got %d, want 5", got)
	}
	if err := tx.RollbackToSavepoint("sp1"); err != nil {
		t.Fatalf("RollbackToSavepoint: %v", err)
	}
	tt2, _ := tx.Table("items")
	if got := count(tt2); got != 3 {
		t.Errorf("post-rollback count: got %d, want 3 (only pre-sp work survives)", got)
	}
}

// TestSavepoint_NestedRollbackTo handles SAVEPOINT inside SAVEPOINT.
// ROLLBACK TO outer must discard inner work and inner-savepoint state.
func TestSavepoint_NestedRollbackTo(t *testing.T) {
	_, tx := freshTxn(t)
	defer tx.Rollback()

	tt, _ := tx.Table("items")
	if err := tx.Savepoint("outer"); err != nil {
		t.Fatalf("Savepoint outer: %v", err)
	}
	tt.Insert(storage.Row{int32(3)})
	if err := tx.Savepoint("inner"); err != nil {
		t.Fatalf("Savepoint inner: %v", err)
	}
	tt.Insert(storage.Row{int32(4)})
	if err := tx.RollbackToSavepoint("outer"); err != nil {
		t.Fatalf("RollbackToSavepoint outer: %v", err)
	}
	// outer remains; ROLLBACK TO inner now must fail (inner was popped).
	if err := tx.RollbackToSavepoint("inner"); err == nil {
		t.Fatal("RollbackToSavepoint inner: want error, got nil")
	}
	tt2, _ := tx.Table("items")
	if got := count(tt2); got != 2 {
		t.Errorf("post-rollback: got %d, want 2 (back to pre-outer)", got)
	}
}

// TestSavepoint_ReleasePops drops the savepoint without restoring;
// later RollbackTo of the released name must fail.
func TestSavepoint_ReleasePops(t *testing.T) {
	_, tx := freshTxn(t)
	defer tx.Rollback()

	tt, _ := tx.Table("items")
	if err := tx.Savepoint("sp"); err != nil {
		t.Fatalf("Savepoint: %v", err)
	}
	tt.Insert(storage.Row{int32(99)})
	if err := tx.ReleaseSavepoint("sp"); err != nil {
		t.Fatalf("ReleaseSavepoint: %v", err)
	}
	// Release doesn't undo the work.
	if got := count(tt); got != 3 {
		t.Errorf("post-release: got %d, want 3", got)
	}
	// And the savepoint is gone.
	err := tx.RollbackToSavepoint("sp")
	var spErr *storage.SavepointError
	if !errors.As(err, &spErr) {
		t.Errorf("RollbackTo released sp: got %v, want SavepointError", err)
	}
}

// TestSavepoint_UnknownNameErrors covers the SAVEPOINT-not-found case
// for both RollbackTo and Release.
func TestSavepoint_UnknownNameErrors(t *testing.T) {
	_, tx := freshTxn(t)
	defer tx.Rollback()
	for _, op := range []struct {
		name string
		fn   func(string) error
	}{
		{"RollbackToSavepoint", tx.RollbackToSavepoint},
		{"ReleaseSavepoint", tx.ReleaseSavepoint},
	} {
		t.Run(op.name, func(t *testing.T) {
			err := op.fn("does-not-exist")
			var spErr *storage.SavepointError
			if !errors.As(err, &spErr) {
				t.Errorf("got %v, want SavepointError", err)
			}
		})
	}
}
