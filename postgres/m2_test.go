package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

// TestM2_CreateInsertSelect is the M2 acceptance test. It exercises the
// minimum slice the milestone calls for: CREATE TABLE, INSERT (multi-
// row), SELECT with WHERE / ORDER BY / LIMIT, and `$N` parameters
// flowing through pgx's extended-query protocol.
func TestM2_CreateInsertSelect(t *testing.T) {
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

	// CREATE TABLE
	if _, err := pool.Exec(ctx, `CREATE TABLE accounts (id int NOT NULL, name text NOT NULL, balance bigint NOT NULL, active bool NOT NULL)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	// INSERT (multi-row)
	if _, err := pool.Exec(ctx, `
		INSERT INTO accounts (id, name, balance, active) VALUES
			($1, $2, $3, $4),
			($5, $6, $7, $8),
			($9, $10, $11, $12),
			($13, $14, $15, $16)
	`,
		int32(1), "alice", int64(100), true,
		int32(2), "bob", int64(50), false,
		int32(3), "carol", int64(200), true,
		int32(4), "dan", int64(75), true,
	); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	// SELECT with WHERE + ORDER BY + LIMIT, all driven by parameters.
	type acct struct {
		ID      int32
		Name    string
		Balance int64
	}
	rows, err := pool.Query(ctx,
		`SELECT id, name, balance FROM accounts WHERE active = $1 AND balance >= $2 ORDER BY balance DESC LIMIT $3`,
		true, int64(75), int64(2),
	)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	got := []acct{}
	for rows.Next() {
		var a acct
		if err := rows.Scan(&a.ID, &a.Name, &a.Balance); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		got = append(got, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	want := []acct{
		{3, "carol", 200},
		{1, "alice", 100},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d (%+v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("row %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestM2_OrderAscWithOffset hits the OFFSET path and ASC order, which
// the main acceptance test doesn't.
func TestM2_OrderAscWithOffset(t *testing.T) {
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

	if _, err := pool.Exec(ctx, `CREATE TABLE nums (n int NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO nums (n) VALUES (5), (1), (4), (2), (3)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	rows, err := pool.Query(ctx, `SELECT n FROM nums ORDER BY n ASC LIMIT 2 OFFSET 1`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	var got []int32
	for rows.Next() {
		var n int32
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		got = append(got, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	want := []int32{2, 3}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("row %d: got %d, want %d", i, got[i], want[i])
		}
	}
}
