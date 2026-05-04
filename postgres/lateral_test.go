package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func lateralPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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
	for _, sql := range []string{
		`CREATE TABLE authors (id int PRIMARY KEY, name text NOT NULL)`,
		`CREATE TABLE posts (id int PRIMARY KEY, author_id int NOT NULL REFERENCES authors(id), title text NOT NULL, score int NOT NULL)`,
		`INSERT INTO authors (id, name) VALUES (1, 'alice'), (2, 'bob'), (3, 'carol')`,
		`INSERT INTO posts (id, author_id, title, score) VALUES
			(10, 1, 'A1', 50),
			(11, 1, 'A2', 90),
			(12, 1, 'A3', 70),
			(20, 2, 'B1', 30),
			(21, 2, 'B2', 60)`,
		// carol has no posts.
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}
	return pool, ctx, func() { pool.Close(); cancel() }
}

// TestLateral_TopPostPerAuthor exercises the canonical "top per
// group" pattern: LEFT JOIN LATERAL (SELECT … ORDER BY … LIMIT 1).
func TestLateral_TopPostPerAuthor(t *testing.T) {
	pool, ctx, cleanup := lateralPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `
		SELECT a.id, a.name, top.title
		FROM authors a
		LEFT JOIN LATERAL (
		    SELECT title FROM posts p
		    WHERE p.author_id = a.id
		    ORDER BY p.score DESC LIMIT 1
		) AS top ON true
		ORDER BY a.id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	type triple struct {
		id   int32
		name string
		top  *string
	}
	var got []triple
	for rows.Next() {
		var tr triple
		if err := rows.Scan(&tr.id, &tr.name, &tr.top); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, tr)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3", len(got))
	}
	if got[0].name != "alice" || got[0].top == nil || *got[0].top != "A2" {
		t.Errorf("alice: got %+v, want {1 alice A2}", got[0])
	}
	if got[1].name != "bob" || got[1].top == nil || *got[1].top != "B2" {
		t.Errorf("bob: got %+v, want {2 bob B2}", got[1])
	}
	if got[2].name != "carol" || got[2].top != nil {
		t.Errorf("carol: got %+v, want {3 carol NULL}", got[2])
	}
}

// TestLateral_CrossJoinLateralEachAuthorAllPosts: CROSS JOIN LATERAL
// expands a per-row subquery into multiple rows. Compare to the
// inner-JOIN form — just exercises the syntax.
func TestLateral_CrossJoinLateralAllPosts(t *testing.T) {
	pool, ctx, cleanup := lateralPool(t)
	defer cleanup()
	rows, err := pool.Query(ctx, `
		SELECT a.id, p.title
		FROM authors a
		CROSS JOIN LATERAL (
		    SELECT title FROM posts WHERE author_id = a.id ORDER BY id
		) AS p
		ORDER BY a.id, p.title`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var titles []string
	for rows.Next() {
		var id int32
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			t.Fatalf("scan: %v", err)
		}
		titles = append(titles, title)
	}
	want := []string{"A1", "A2", "A3", "B1", "B2"}
	if len(titles) != len(want) {
		t.Fatalf("got %v, want %v", titles, want)
	}
	for i := range want {
		if titles[i] != want[i] {
			t.Errorf("[%d]: got %q, want %q", i, titles[i], want[i])
		}
	}
}
