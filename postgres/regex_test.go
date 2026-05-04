package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func regexPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
	t.Helper()
	srv, err := postgres.Start(t)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	pool, err := pgxpool.New(ctx, srv.DSN())
	if err != nil {
		cancel()
		t.Fatalf("pgxpool.New: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, name text NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO t (id, name) VALUES (1, 'Alice'), (2, 'Bob'), (3, 'alfred'), (4, 'CAROL')`,
	); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

func selectIDsRegex(t *testing.T, pool *pgxpool.Pool, ctx context.Context, sql string) []int32 {
	t.Helper()
	rows, err := pool.Query(ctx, sql)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	defer rows.Close()
	var got []int32
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, id)
	}
	return got
}

func TestRegex_CaseSensitive(t *testing.T) {
	pool, ctx, cleanup := regexPool(t)
	defer cleanup()
	got := selectIDsRegex(t, pool, ctx, `SELECT id FROM t WHERE name ~ '^A' ORDER BY id`)
	if len(got) != 1 || got[0] != 1 {
		t.Errorf("got %v, want [1]", got)
	}
}

func TestRegex_CaseInsensitive(t *testing.T) {
	pool, ctx, cleanup := regexPool(t)
	defer cleanup()
	got := selectIDsRegex(t, pool, ctx, `SELECT id FROM t WHERE name ~* '^a' ORDER BY id`)
	if len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Errorf("got %v, want [1 3]", got)
	}
}

func TestRegex_NotMatch(t *testing.T) {
	pool, ctx, cleanup := regexPool(t)
	defer cleanup()
	got := selectIDsRegex(t, pool, ctx, `SELECT id FROM t WHERE name !~ '^A' ORDER BY id`)
	if len(got) != 3 || got[0] != 2 {
		t.Errorf("got %v, want [2 3 4]", got)
	}
}

func TestRegex_NotMatchCaseInsensitive(t *testing.T) {
	pool, ctx, cleanup := regexPool(t)
	defer cleanup()
	got := selectIDsRegex(t, pool, ctx, `SELECT id FROM t WHERE name !~* '^a' ORDER BY id`)
	if len(got) != 2 || got[0] != 2 || got[1] != 4 {
		t.Errorf("got %v, want [2 4]", got)
	}
}

func TestRegex_AnchoredPattern(t *testing.T) {
	pool, ctx, cleanup := regexPool(t)
	defer cleanup()
	got := selectIDsRegex(t, pool, ctx, `SELECT id FROM t WHERE name ~ 'l.+d$' ORDER BY id`)
	if len(got) != 1 || got[0] != 3 {
		t.Errorf("got %v, want [3]", got)
	}
}
