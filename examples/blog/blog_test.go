// Package blog_test exercises the sqlc-generated query layer against
// an embedded pgmem-go server. No external Postgres required.
package blog_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/examples/blog/db"
	"github.com/kaeawc/pgmem-go/postgres"
)

// setup spins up pgmem-go, applies schema.sql, and returns a sqlc
// query handle plus a cleanup func. The raw pool is also returned so
// individual tests can drop down to ad-hoc queries when they need
// shapes the generated queries don't cover.
func setup(t *testing.T) (*db.Queries, *pgxpool.Pool, context.Context, func()) {
	t.Helper()
	srv, err := postgres.Start(t)
	if err != nil {
		t.Fatalf("postgres.Start: %v", err)
	}
	pinned := time.Date(2024, 7, 15, 12, 0, 0, 0, time.UTC)
	srv.SetNow(pinned)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	pool, err := pgxpool.New(ctx, srv.DSN())
	if err != nil {
		cancel()
		t.Fatalf("pgxpool.New: %v", err)
	}
	schemaPath := filepath.Join("schema.sql")
	schemaBytes, err := os.ReadFile(schemaPath)
	if err != nil {
		pool.Close()
		cancel()
		t.Fatalf("read %s: %v", schemaPath, err)
	}
	for _, stmt := range splitStatements(string(schemaBytes)) {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			pool.Close()
			cancel()
			t.Fatalf("schema %q: %v", stmt, err)
		}
	}
	return db.New(pool), pool, ctx, func() { pool.Close(); cancel() }
}

// splitStatements is a small SQL splitter that breaks the schema file
// on top-level semicolons. Schema files in these examples don't use
// quoted identifiers or semicolons inside string literals, so naive
// splitting is fine.
func splitStatements(s string) []string {
	var out []string
	for _, raw := range strings.Split(s, ";") {
		stmt := strings.TrimSpace(raw)
		if stmt == "" {
			continue
		}
		out = append(out, stmt)
	}
	return out
}

func TestUserCRUD(t *testing.T) {
	q, _, ctx, cleanup := setup(t)
	defer cleanup()
	now := time.Date(2024, 7, 15, 12, 0, 0, 0, time.UTC)
	created, err := q.CreateUser(ctx, db.CreateUserParams{
		Email: "ada@example.com", Name: "Ada", CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if created.Email != "ada@example.com" {
		t.Errorf("email = %q", created.Email)
	}
	got, err := q.GetUser(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.ID != created.ID || got.Name != "Ada" {
		t.Errorf("got %+v", got)
	}
	all, err := q.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("ListUsers: got %d, want 1", len(all))
	}
}

func TestPostsAndComments(t *testing.T) {
	q, _, ctx, cleanup := setup(t)
	defer cleanup()
	now := time.Date(2024, 7, 15, 12, 0, 0, 0, time.UTC)

	ada, err := q.CreateUser(ctx, db.CreateUserParams{Email: "ada@example.com", Name: "Ada", CreatedAt: now})
	if err != nil {
		t.Fatalf("CreateUser ada: %v", err)
	}
	bob, err := q.CreateUser(ctx, db.CreateUserParams{Email: "bob@example.com", Name: "Bob", CreatedAt: now})
	if err != nil {
		t.Fatalf("CreateUser bob: %v", err)
	}

	post, err := q.CreatePost(ctx, db.CreatePostParams{
		AuthorID: ada.ID, Title: "First", Body: "hello world",
		Published: false, CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	if post.Published {
		t.Errorf("post should be draft initially")
	}

	// Post is hidden until published.
	pub1, err := q.ListPublishedPosts(ctx, db.ListPublishedPostsParams{Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("ListPublishedPosts pre: %v", err)
	}
	if len(pub1) != 0 {
		t.Errorf("expected no published posts, got %d", len(pub1))
	}

	published, err := q.PublishPost(ctx, post.ID)
	if err != nil {
		t.Fatalf("PublishPost: %v", err)
	}
	if !published.Published {
		t.Errorf("PublishPost RETURNING: published = %v, want true", published.Published)
	}
	got, err := q.GetPost(ctx, post.ID)
	if err != nil {
		t.Fatalf("GetPost: %v", err)
	}
	if !got.Published {
		t.Errorf("GetPost after publish: published = %v, want true", got.Published)
	}

	pub2, err := q.ListPublishedPosts(ctx, db.ListPublishedPostsParams{Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("ListPublishedPosts post: %v", err)
	}
	if len(pub2) != 1 || pub2[0].AuthorName != "Ada" {
		t.Errorf("got %+v", pub2)
	}

	// Two comments, one from each user.
	if _, err := q.CreateComment(ctx, db.CreateCommentParams{
		PostID: post.ID, AuthorID: bob.ID, Body: "nice", CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateComment 1: %v", err)
	}
	if _, err := q.CreateComment(ctx, db.CreateCommentParams{
		PostID: post.ID, AuthorID: ada.ID, Body: "thanks", CreatedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("CreateComment 2: %v", err)
	}

	comments, err := q.ListCommentsForPost(ctx, post.ID)
	if err != nil {
		t.Fatalf("ListCommentsForPost: %v", err)
	}
	if len(comments) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(comments))
	}
	if comments[0].AuthorName != "Bob" || comments[1].AuthorName != "Ada" {
		t.Errorf("comment authors: got %v / %v, want Bob / Ada", comments[0].AuthorName, comments[1].AuthorName)
	}

	// Aggregate count.
	counts, err := q.CountCommentsPerPost(ctx)
	if err != nil {
		t.Fatalf("CountCommentsPerPost: %v", err)
	}
	if len(counts) != 1 || counts[0].PostID != post.ID || counts[0].CommentCount != 2 {
		t.Errorf("got %+v", counts)
	}

	// Correlated scalar subquery: count comments via the post id
	// referenced from the outer query.
	withCount, err := q.PostWithCommentCount(ctx, post.ID)
	if err != nil {
		t.Fatalf("PostWithCommentCount: %v", err)
	}
	if withCount.CommentCount != 2 {
		t.Errorf("subquery count = %d, want 2", withCount.CommentCount)
	}
}

func TestPagination(t *testing.T) {
	q, _, ctx, cleanup := setup(t)
	defer cleanup()
	now := time.Date(2024, 7, 15, 12, 0, 0, 0, time.UTC)

	user, err := q.CreateUser(ctx, db.CreateUserParams{Email: "ada@example.com", Name: "Ada", CreatedAt: now})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := q.CreatePost(ctx, db.CreatePostParams{
			AuthorID: user.ID, Title: "Post", Body: "...", Published: true,
			CreatedAt: now.Add(time.Duration(i) * time.Hour),
		}); err != nil {
			t.Fatalf("CreatePost %d: %v", i, err)
		}
	}
	page1, err := q.ListPublishedPosts(ctx, db.ListPublishedPostsParams{Limit: 2, Offset: 0})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 2 {
		t.Errorf("page1: got %d, want 2", len(page1))
	}
	page2, err := q.ListPublishedPosts(ctx, db.ListPublishedPostsParams{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 2 {
		t.Errorf("page2: got %d, want 2", len(page2))
	}
	page3, err := q.ListPublishedPosts(ctx, db.ListPublishedPostsParams{Limit: 2, Offset: 4})
	if err != nil {
		t.Fatalf("page3: %v", err)
	}
	if len(page3) != 1 {
		t.Errorf("page3: got %d, want 1", len(page3))
	}
}

func TestDeleteFKReject(t *testing.T) {
	q, _, ctx, cleanup := setup(t)
	defer cleanup()
	now := time.Date(2024, 7, 15, 12, 0, 0, 0, time.UTC)
	user, err := q.CreateUser(ctx, db.CreateUserParams{Email: "ada@example.com", Name: "Ada", CreatedAt: now})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	post, err := q.CreatePost(ctx, db.CreatePostParams{
		AuthorID: user.ID, Title: "x", Body: "y", Published: true, CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	if _, err := q.CreateComment(ctx, db.CreateCommentParams{
		PostID: post.ID, AuthorID: user.ID, Body: "comment", CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	// Deleting a post that still has comments should violate the FK.
	if err := q.DeletePost(ctx, post.ID); err == nil {
		t.Errorf("expected FK error, got nil")
	}
}
