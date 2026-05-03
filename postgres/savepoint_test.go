package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kaeawc/pgmem-go/postgres"
)

// savepointSetup pins a single connection (so BEGIN/SAVEPOINT see the
// same conn) and seeds an `items` table.
func savepointSetup(t *testing.T) (*pgx.Conn, context.Context, func()) {
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
	if _, err := conn.Exec(ctx, `CREATE TABLE items (id int PRIMARY KEY, label text NOT NULL)`); err != nil {
		conn.Close(ctx)
		cancel()
		t.Fatalf("CREATE: %v", err)
	}
	return conn, ctx, func() { conn.Close(context.Background()); cancel() }
}

func itemCount(t *testing.T, conn *pgx.Conn, ctx context.Context) int {
	t.Helper()
	rows, err := conn.Query(ctx, `SELECT id FROM items`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id int32
		_ = rows.Scan(&id)
		n++
	}
	return n
}

// TestSavepoint_RollbackToRecoversFromPoisonedBlock: a SAVEPOINT
// before a UNIQUE-violating INSERT lets ROLLBACK TO clear the failed
// state without ending the txn — the most useful sqlc-style pattern.
func TestSavepoint_RollbackToRecoversFromPoisonedBlock(t *testing.T) {
	conn, ctx, cleanup := savepointSetup(t)
	defer cleanup()

	if _, err := conn.Exec(ctx, `BEGIN`); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO items (id, label) VALUES (1, 'a')`); err != nil {
		t.Fatalf("INSERT 1: %v", err)
	}
	if _, err := conn.Exec(ctx, `SAVEPOINT before_dup`); err != nil {
		t.Fatalf("SAVEPOINT: %v", err)
	}
	// Failing statement poisons the block.
	_, err := conn.Exec(ctx, `INSERT INTO items (id, label) VALUES (1, 'dup')`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("dup: got %v, want 23505", err)
	}
	// Without ROLLBACK TO, this would 25P02. With it, we recover.
	if _, err := conn.Exec(ctx, `ROLLBACK TO SAVEPOINT before_dup`); err != nil {
		t.Fatalf("ROLLBACK TO: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO items (id, label) VALUES (2, 'b')`); err != nil {
		t.Fatalf("INSERT after recovery: %v", err)
	}
	if _, err := conn.Exec(ctx, `COMMIT`); err != nil {
		t.Fatalf("COMMIT: %v", err)
	}
	// 2 rows: id=1 (pre-savepoint) + id=2 (post-recovery). The dup is gone.
	if got := itemCount(t, conn, ctx); got != 2 {
		t.Errorf("count: got %d, want 2", got)
	}
}

// TestSavepoint_ReleaseDoesNotUndoWork covers the release-as-discard
// path: the work between SAVEPOINT and RELEASE survives.
func TestSavepoint_ReleaseDoesNotUndoWork(t *testing.T) {
	conn, ctx, cleanup := savepointSetup(t)
	defer cleanup()

	if _, err := conn.Exec(ctx, `BEGIN`); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	if _, err := conn.Exec(ctx, `SAVEPOINT s1`); err != nil {
		t.Fatalf("SAVEPOINT: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO items (id, label) VALUES (1, 'a')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if _, err := conn.Exec(ctx, `RELEASE SAVEPOINT s1`); err != nil {
		t.Fatalf("RELEASE: %v", err)
	}
	if _, err := conn.Exec(ctx, `COMMIT`); err != nil {
		t.Fatalf("COMMIT: %v", err)
	}
	if got := itemCount(t, conn, ctx); got != 1 {
		t.Errorf("count: got %d, want 1", got)
	}
}

// TestSavepoint_OutsideTxIsRejected: SAVEPOINT outside an explicit
// BEGIN block returns SQLSTATE 25P01.
func TestSavepoint_OutsideTxIsRejected(t *testing.T) {
	conn, ctx, cleanup := savepointSetup(t)
	defer cleanup()

	_, err := conn.Exec(ctx, `SAVEPOINT s1`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "25P01" {
		t.Errorf("got %v, want 25P01", err)
	}
}

// TestSavepoint_UnknownNameRejects: ROLLBACK TO a nonexistent name
// surfaces SQLSTATE 3B001.
func TestSavepoint_UnknownNameRejects(t *testing.T) {
	conn, ctx, cleanup := savepointSetup(t)
	defer cleanup()
	if _, err := conn.Exec(ctx, `BEGIN`); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	defer func() { _, _ = conn.Exec(ctx, `ROLLBACK`) }()

	_, err := conn.Exec(ctx, `ROLLBACK TO SAVEPOINT does_not_exist`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "3B001" {
		t.Errorf("got %v, want 3B001", err)
	}
}
