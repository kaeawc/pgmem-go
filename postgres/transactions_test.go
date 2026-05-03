package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

// txnSetup spins up a server with a fresh `accounts` table and one
// pinned connection so we can use BEGIN / COMMIT / ROLLBACK without
// pgxpool handing us a different connection mid-test.
func txnSetup(t *testing.T) (*pgx.Conn, context.Context, func()) {
	t.Helper()
	srv, err := postgres.Start(t)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	conn, err := pgx.Connect(ctx, srv.DSN())
	if err != nil {
		cancel()
		t.Fatalf("pgx.Connect: %v", err)
	}
	if _, err := conn.Exec(ctx, `CREATE TABLE accounts (id int PRIMARY KEY, name text NOT NULL)`); err != nil {
		conn.Close(ctx)
		cancel()
		t.Fatalf("CREATE: %v", err)
	}
	cleanup := func() {
		conn.Close(context.Background())
		cancel()
	}
	return conn, ctx, cleanup
}

func countAccounts(t *testing.T, conn *pgx.Conn, ctx context.Context) int {
	t.Helper()
	rows, err := conn.Query(ctx, `SELECT id FROM accounts`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	return n
}

// TestTxn_RollbackUndoesInsert: BEGIN, INSERT, ROLLBACK — the row
// must not be in the table afterwards.
func TestTxn_RollbackUndoesInsert(t *testing.T) {
	conn, ctx, cleanup := txnSetup(t)
	defer cleanup()

	if _, err := conn.Exec(ctx, `BEGIN`); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO accounts (id, name) VALUES (1, 'alice')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	// Inside the txn the row exists.
	if got := countAccounts(t, conn, ctx); got != 1 {
		t.Errorf("in-tx count: got %d, want 1", got)
	}
	if _, err := conn.Exec(ctx, `ROLLBACK`); err != nil {
		t.Fatalf("ROLLBACK: %v", err)
	}
	// After rollback it's gone.
	if got := countAccounts(t, conn, ctx); got != 0 {
		t.Errorf("post-rollback count: got %d, want 0", got)
	}
}

// TestTxn_CommitPersistsInsert: BEGIN, INSERT, COMMIT — the row stays.
func TestTxn_CommitPersistsInsert(t *testing.T) {
	conn, ctx, cleanup := txnSetup(t)
	defer cleanup()

	if _, err := conn.Exec(ctx, `BEGIN`); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO accounts (id, name) VALUES (1, 'alice')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if _, err := conn.Exec(ctx, `COMMIT`); err != nil {
		t.Fatalf("COMMIT: %v", err)
	}
	if got := countAccounts(t, conn, ctx); got != 1 {
		t.Errorf("post-commit count: got %d, want 1", got)
	}
}

// TestTxn_AbortedStatePoisonsBlock: a failed statement inside a tx
// puts the conn into 'E' state — subsequent statements until ROLLBACK
// must error with SQLSTATE 25P02.
func TestTxn_AbortedStatePoisonsBlock(t *testing.T) {
	conn, ctx, cleanup := txnSetup(t)
	defer cleanup()

	if _, err := conn.Exec(ctx, `BEGIN`); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO accounts (id, name) VALUES (1, 'alice')`); err != nil {
		t.Fatalf("first INSERT: %v", err)
	}
	// Duplicate id triggers UNIQUE violation (PRIMARY KEY).
	_, err := conn.Exec(ctx, `INSERT INTO accounts (id, name) VALUES (1, 'alice-clone')`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("dup INSERT: got %v, want 23505", err)
	}
	// Now any further statement should fail with 25P02 until ROLLBACK.
	_, err = conn.Exec(ctx, `INSERT INTO accounts (id, name) VALUES (2, 'bob')`)
	if !errors.As(err, &pgErr) || pgErr.Code != "25P02" {
		t.Fatalf("post-failure stmt: got %v, want 25P02", err)
	}
	if _, err := conn.Exec(ctx, `ROLLBACK`); err != nil {
		t.Fatalf("ROLLBACK: %v", err)
	}
	// Fresh statement after rollback works again.
	if _, err := conn.Exec(ctx, `INSERT INTO accounts (id, name) VALUES (2, 'bob')`); err != nil {
		t.Fatalf("post-rollback INSERT: %v", err)
	}
	if got := countAccounts(t, conn, ctx); got != 1 {
		t.Errorf("count: got %d, want 1 (only bob — first txn's alice was rolled back)", got)
	}
}

// TestTxn_CommitAfterErrorActsAsRollback: PG behavior — issuing COMMIT
// while the tx is in failed state acts like ROLLBACK and the
// CommandComplete tag reflects that.
func TestTxn_CommitAfterErrorActsAsRollback(t *testing.T) {
	conn, ctx, cleanup := txnSetup(t)
	defer cleanup()

	if _, err := conn.Exec(ctx, `BEGIN`); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO accounts (id, name) VALUES (1, 'alice')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	_, _ = conn.Exec(ctx, `INSERT INTO accounts (id, name) VALUES (1, 'dup')`) // poisons the tx
	tag, err := conn.Exec(ctx, `COMMIT`)
	if err != nil {
		t.Fatalf("COMMIT: %v", err)
	}
	if tag.String() != "ROLLBACK" {
		t.Errorf("CommandComplete tag: got %q, want ROLLBACK", tag.String())
	}
	if got := countAccounts(t, conn, ctx); got != 0 {
		t.Errorf("count: got %d, want 0 (commit-after-error must rollback)", got)
	}
}

// TestTxn_ImplicitAutocommit confirms statements outside an explicit
// BEGIN keep auto-committing, the way every prior test in the suite
// has assumed.
func TestTxn_ImplicitAutocommit(t *testing.T) {
	srv, err := postgres.Start(t)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, srv.DSN())
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `CREATE TABLE bare (n int NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO bare (n) VALUES (1), (2), (3)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	rows, err := pool.Query(ctx, `SELECT n FROM bare`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	count := 0
	for rows.Next() {
		var n int32
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if count != 3 {
		t.Errorf("count: got %d, want 3", count)
	}
}
