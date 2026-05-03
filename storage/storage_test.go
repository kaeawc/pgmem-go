package storage_test

import (
	"context"
	"testing"

	"github.com/kaeawc/pgmem-go/storage"
)

func newEngineWithItems(t *testing.T) storage.Engine {
	t.Helper()
	eng := storage.NewEngine()
	eng.CreateTable("items", 2)
	t1, _ := eng.Table("items")
	t1.Insert(storage.Row{int32(1), "alpha"})
	t1.Insert(storage.Row{int32(2), "beta"})
	return eng
}

func ids(rows []storage.Row) []int32 {
	out := make([]int32, len(rows))
	for i, r := range rows {
		out[i] = r[0].(int32)
	}
	return out
}

// TestTxn_WritesNotVisibleUntilCommit: an Insert inside a txn does not
// appear in the canonical engine table until the txn commits. Other
// txns (and direct Engine.Table calls) keep seeing the snapshot at
// their start time.
func TestTxn_WritesNotVisibleUntilCommit(t *testing.T) {
	eng := newEngineWithItems(t)

	txn, _ := eng.Begin(context.Background())
	tt, _ := txn.Table("items")
	tt.Insert(storage.Row{int32(3), "gamma"})

	// In-tx view: 3 rows.
	if got := len(tt.Rows()); got != 3 {
		t.Errorf("in-tx rows: got %d, want 3", got)
	}
	// Engine view: still 2 rows.
	canon, _ := eng.Table("items")
	if got := len(canon.Rows()); got != 2 {
		t.Errorf("canonical rows pre-commit: got %d, want 2", got)
	}

	if err := txn.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := len(canon.Rows()); got != 3 {
		t.Errorf("canonical rows post-commit: got %d, want 3", got)
	}
}

// TestTxn_RollbackDiscards confirms Rollback drops all writes the txn
// made — the engine returns to the pre-txn state.
func TestTxn_RollbackDiscards(t *testing.T) {
	eng := newEngineWithItems(t)
	txn, _ := eng.Begin(context.Background())
	tt, _ := txn.Table("items")
	tt.Insert(storage.Row{int32(99), "ignore"})
	tt.Mutate(func(rows []storage.Row) []storage.Row { return rows[:0] })
	if got := len(tt.Rows()); got != 0 {
		t.Fatalf("in-tx after mutate: got %d, want 0", got)
	}
	if err := txn.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	canon, _ := eng.Table("items")
	if got := ids(canon.Rows()); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("canonical after rollback: got %v, want [1 2]", got)
	}
}

// TestTxn_SnapshotIsolation: a second txn started before commit sees
// only the original rows even after the first commits.
func TestTxn_SnapshotIsolation(t *testing.T) {
	eng := newEngineWithItems(t)

	txnA, _ := eng.Begin(context.Background())
	ttA, _ := txnA.Table("items")
	ttA.Insert(storage.Row{int32(3), "gamma"})

	txnB, _ := eng.Begin(context.Background())
	ttB, _ := txnB.Table("items")
	if got := len(ttB.Rows()); got != 2 {
		t.Errorf("txnB view (snapshot pre-commit-of-A): got %d, want 2", got)
	}

	if err := txnA.Commit(); err != nil {
		t.Fatalf("Commit A: %v", err)
	}
	// txnB still snapshot-isolated: doesn't see A's commit.
	if got := len(ttB.Rows()); got != 2 {
		t.Errorf("txnB view (post-commit-of-A): got %d, want 2 (snapshot)", got)
	}
	_ = txnB.Rollback()

	canon, _ := eng.Table("items")
	if got := len(canon.Rows()); got != 3 {
		t.Errorf("canonical post: got %d, want 3", got)
	}
}

// TestTxn_RollbackIsIdempotent: calling Rollback on a closed txn is a
// no-op (and so is Commit-after-Rollback). Matters for the wire layer's
// defer Rollback safety net.
func TestTxn_RollbackIsIdempotent(t *testing.T) {
	eng := newEngineWithItems(t)
	txn, _ := eng.Begin(context.Background())
	if err := txn.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := txn.Rollback(); err != nil {
		t.Errorf("Rollback after Commit: got %v, want nil", err)
	}
}
