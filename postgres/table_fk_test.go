package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func tableFKPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	return pool, ctx, func() { pool.Close(); cancel() }
}

func TestTableFK_Basic(t *testing.T) {
	pool, ctx, cleanup := tableFKPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE users (id bigint PRIMARY KEY, name text NOT NULL)`); err != nil {
		t.Fatalf("CREATE users: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE posts (
		  id        bigint PRIMARY KEY,
		  author_id bigint NOT NULL,
		  title     text   NOT NULL,
		  FOREIGN KEY (author_id) REFERENCES users(id)
		)
	`); err != nil {
		t.Fatalf("CREATE posts: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, name) VALUES (1, 'alice')`); err != nil {
		t.Fatalf("INSERT users: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO posts (id, author_id, title) VALUES (10, 1, 'hi')`); err != nil {
		t.Fatalf("INSERT post: %v", err)
	}
	// Missing parent should fail.
	if _, err := pool.Exec(ctx, `INSERT INTO posts (id, author_id, title) VALUES (11, 99, 'bad')`); err == nil {
		t.Fatalf("expected FK violation")
	}
}

func TestTableFK_Named(t *testing.T) {
	pool, ctx, cleanup := tableFKPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE users (id bigint PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE users: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE posts (
		  id        bigint PRIMARY KEY,
		  author_id bigint NOT NULL,
		  CONSTRAINT posts_author_fk FOREIGN KEY (author_id) REFERENCES users(id)
		)
	`); err != nil {
		t.Fatalf("CREATE posts: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO posts (id, author_id) VALUES (1, 99)`); err == nil {
		t.Fatalf("expected FK violation")
	}
}

func TestTableFK_OnDeleteCascade(t *testing.T) {
	pool, ctx, cleanup := tableFKPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE users (id bigint PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE users: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE posts (
		  id        bigint PRIMARY KEY,
		  author_id bigint NOT NULL,
		  CONSTRAINT posts_author_fk FOREIGN KEY (author_id) REFERENCES users(id) ON DELETE CASCADE
		)
	`); err != nil {
		t.Fatalf("CREATE posts: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users (id) VALUES (1), (2)`); err != nil {
		t.Fatalf("INSERT users: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO posts (id, author_id) VALUES (10, 1), (11, 2)`); err != nil {
		t.Fatalf("INSERT posts: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = 1`); err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	var n int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM posts`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("after cascade got %d posts, want 1", n)
	}
}

func TestTableFK_UnknownColumn(t *testing.T) {
	pool, ctx, cleanup := tableFKPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE users (id bigint PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE users: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE posts (
		  id bigint PRIMARY KEY,
		  FOREIGN KEY (author_id) REFERENCES users(id)
		)
	`); err == nil {
		t.Fatalf("expected error for FK on missing column")
	}
}

func TestTableFK_MultiColumnRejected(t *testing.T) {
	pool, ctx, cleanup := tableFKPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE users (id bigint PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE users: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE posts (
		  a bigint NOT NULL,
		  b bigint NOT NULL,
		  FOREIGN KEY (a, b) REFERENCES users(id, id)
		)
	`); err == nil {
		t.Fatalf("expected parse error for multi-column FK")
	}
}
