// Package chat_test exercises the sqlc-generated query layer against
// an embedded pgmem-go server. The schema covers UNION ALL across
// disjoint message tables, EXISTS-based membership, self-join for
// thread replies, and ON CONFLICT DO NOTHING on a composite key.
package chat_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/examples/chat/db"
	"github.com/kaeawc/pgmem-go/postgres"
)

func setup(t *testing.T) (*db.Queries, context.Context, func()) {
	t.Helper()
	srv, err := postgres.Start(t)
	if err != nil {
		t.Fatalf("postgres.Start: %v", err)
	}
	srv.SetNow(time.Date(2024, 7, 15, 12, 0, 0, 0, time.UTC))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	pool, err := pgxpool.New(ctx, srv.DSN())
	if err != nil {
		cancel()
		t.Fatalf("pgxpool.New: %v", err)
	}
	schemaBytes, err := os.ReadFile(filepath.Join("schema.sql"))
	if err != nil {
		pool.Close()
		cancel()
		t.Fatalf("read schema: %v", err)
	}
	for _, stmt := range strings.Split(string(schemaBytes), ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := pool.Exec(ctx, stmt); err != nil {
			pool.Close()
			cancel()
			t.Fatalf("schema %q: %v", stmt, err)
		}
	}
	return db.New(pool), ctx, func() { pool.Close(); cancel() }
}

// fixture creates a single room with two users (alice + bob), one
// subscription for alice, and a couple of messages each kind.
func fixture(t *testing.T, q *db.Queries, ctx context.Context) (aliceID, bobID, roomID int64, parentMsgID int64) {
	t.Helper()
	alice, err := q.AddUser(ctx, "alice")
	if err != nil {
		t.Fatalf("AddUser alice: %v", err)
	}
	bob, err := q.AddUser(ctx, "bob")
	if err != nil {
		t.Fatalf("AddUser bob: %v", err)
	}
	room, err := q.AddRoom(ctx, "general")
	if err != nil {
		t.Fatalf("AddRoom: %v", err)
	}
	if err := q.Subscribe(ctx, db.SubscribeParams{UserID: alice.ID, RoomID: room.ID}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	now := time.Date(2024, 7, 15, 12, 0, 0, 0, time.UTC)
	parent, err := q.PostMessage(ctx, db.PostMessageParams{
		RoomID: room.ID, AuthorID: alice.ID,
		ParentID: nil, Body: "hello world", SentAt: now,
	})
	if err != nil {
		t.Fatalf("PostMessage parent: %v", err)
	}
	if _, err := q.PostMessage(ctx, db.PostMessageParams{
		RoomID: room.ID, AuthorID: bob.ID,
		ParentID: &parent.ID, Body: "hi back", SentAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("PostMessage reply: %v", err)
	}
	if _, err := q.PostSystemMessage(ctx, db.PostSystemMessageParams{
		RoomID: room.ID, Body: "user alice joined",
		SentAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("PostSystemMessage: %v", err)
	}
	return alice.ID, bob.ID, room.ID, parent.ID
}

// TestRoomFeed_UnionAll exercises UNION ALL across messages and
// system_messages plus pagination with LIMIT/OFFSET.
func TestRoomFeed_UnionAll(t *testing.T) {
	q, ctx, cleanup := setup(t)
	defer cleanup()
	_, _, roomID, _ := fixture(t, q, ctx)

	rows, err := q.RoomFeed(ctx, db.RoomFeedParams{
		RoomID: roomID, Limit: 10, Offset: 0,
	})
	if err != nil {
		t.Fatalf("RoomFeed: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d feed rows, want 3", len(rows))
	}
	// ORDER BY sent_at DESC: reply first, then parent, then system.
	wantSources := []string{"user", "user", "system"}
	for i, w := range wantSources {
		if string(rows[i].Source) != w {
			t.Errorf("row %d source = %v, want %s", i, rows[i].Source, w)
		}
	}
	// Pagination: limit 1 offset 1 should return exactly the parent
	// (the second-most-recent row).
	page, err := q.RoomFeed(ctx, db.RoomFeedParams{RoomID: roomID, Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("RoomFeed page: %v", err)
	}
	if len(page) != 1 || page[0].Body != "hello world" {
		t.Errorf("page = %+v", page)
	}
}

// TestSubscribedRooms exercises an EXISTS subquery for membership
// checks.
func TestSubscribedRooms(t *testing.T) {
	q, ctx, cleanup := setup(t)
	defer cleanup()
	aliceID, bobID, roomID, _ := fixture(t, q, ctx)

	got, err := q.SubscribedRooms(ctx, aliceID)
	if err != nil {
		t.Fatalf("SubscribedRooms alice: %v", err)
	}
	if len(got) != 1 || got[0].ID != roomID {
		t.Errorf("alice rooms: got %+v", got)
	}
	gotBob, err := q.SubscribedRooms(ctx, bobID)
	if err != nil {
		t.Fatalf("SubscribedRooms bob: %v", err)
	}
	if len(gotBob) != 0 {
		t.Errorf("bob rooms: got %d, want 0", len(gotBob))
	}
}

// TestSubscribe_OnConflictDoNothing confirms ON CONFLICT on the
// composite primary key (user_id, room_id) is a no-op for a duplicate
// subscription.
func TestSubscribe_OnConflictDoNothing(t *testing.T) {
	q, ctx, cleanup := setup(t)
	defer cleanup()
	aliceID, _, roomID, _ := fixture(t, q, ctx)

	// Same row a second time — should not error and should not create
	// a duplicate.
	if err := q.Subscribe(ctx, db.SubscribeParams{UserID: aliceID, RoomID: roomID}); err != nil {
		t.Fatalf("duplicate Subscribe: %v", err)
	}
	rooms, err := q.SubscribedRooms(ctx, aliceID)
	if err != nil {
		t.Fatalf("SubscribedRooms: %v", err)
	}
	if len(rooms) != 1 {
		t.Errorf("got %d subscribed rooms, want 1", len(rooms))
	}
}

// TestReplyThread exercises a self-join (messages JOIN messages
// parent ON parent.id = m.parent_id) with a nullable parent_id.
func TestReplyThread(t *testing.T) {
	q, ctx, cleanup := setup(t)
	defer cleanup()
	_, _, _, parentID := fixture(t, q, ctx)

	rows, err := q.ReplyThread(ctx, &parentID)
	if err != nil {
		t.Fatalf("ReplyThread: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].ReplyBody != "hi back" || rows[0].ParentBody != "hello world" {
		t.Errorf("got %+v, want reply='hi back' parent='hello world'", rows[0])
	}
}
