package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

// TestNamedStatementCache_BindResolvesByName covers the regression
// where pgmem-go bound every Bind against the connection's last
// Parse result, regardless of the statement name pgx specified.
// pgxpool keeps a per-connection statement cache and reuses names —
// so when an UPDATE was the most recent Parse but a Bind referenced
// a previously-cached SELECT, the SELECT bound against the wrong
// plan and silently returned no rows.
func TestNamedStatementCache_BindResolvesByName(t *testing.T) {
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

	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, flag bool NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO t (id, flag) VALUES (1, false)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	// Hit a parameterised SELECT first to populate pgx's cache.
	queryFlag := func() bool {
		var got bool
		if err := pool.QueryRow(ctx, `SELECT flag FROM t WHERE id = $1`, int32(1)).Scan(&got); err != nil {
			t.Fatalf("query: %v", err)
		}
		return got
	}
	if queryFlag() {
		t.Fatal("flag should start false")
	}

	// Different statement, also cached. With the bug, this last
	// Parse'd plan would be the one Bind resolves to on the next
	// SELECT call.
	if _, err := pool.Exec(ctx, `UPDATE t SET flag = true WHERE id = $1`, int32(1)); err != nil {
		t.Fatalf("update: %v", err)
	}

	// Re-run the SELECT. Without the named-cache fix this returned
	// the pre-update value (or no rows at all).
	if !queryFlag() {
		t.Errorf("expected flag=true after UPDATE, got false")
	}
}
