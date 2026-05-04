// Package events_test exercises the sqlc-generated query layer
// against an embedded pgmem-go server. The schema centres on a jsonb
// payload column so the tests cover @>, ->>, regex, date_trunc, and
// extract(epoch FROM ...).
package events_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/examples/events/db"
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

func seed(t *testing.T, q *db.Queries, ctx context.Context) {
	t.Helper()
	base := time.Date(2024, 7, 15, 9, 30, 0, 0, time.UTC)
	for i, p := range []db.RecordEventParams{
		{
			Kind: "login", Body: []byte(`{"user_id":"u1","ip":"10.0.0.1"}`),
			Message: "user u1 signed in", CreatedAt: base,
		},
		{
			Kind: "login", Body: []byte(`{"user_id":"u2","ip":"10.0.0.2"}`),
			Message: "user u2 signed in", CreatedAt: base.Add(20 * time.Minute),
		},
		{
			Kind: "error", Body: []byte(`{"code":500,"path":"/api/x"}`),
			Message: "ERROR: handler panicked", CreatedAt: base.Add(2 * time.Hour),
		},
		{
			Kind: "purchase", Body: []byte(`{"user_id":"u1","amount":42}`),
			Message: "u1 bought widget", CreatedAt: base.Add(3 * time.Hour),
		},
	} {
		if _, err := q.RecordEvent(ctx, p); err != nil {
			t.Fatalf("seed[%d]: %v", i, err)
		}
	}
}

// TestEventsByPayloadKey exercises the jsonb @> containment operator.
// Filtering for `{"user_id":"u1"}` should match every row whose body
// has that pair at the top level.
func TestEventsByPayloadKey(t *testing.T) {
	q, ctx, cleanup := setup(t)
	defer cleanup()
	seed(t, q, ctx)
	got, err := q.EventsByPayloadKey(ctx, []byte(`{"user_id":"u1"}`))
	if err != nil {
		t.Fatalf("EventsByPayloadKey: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2 (login + purchase by u1)", len(got))
	}
	for _, e := range got {
		if !strings.Contains(string(e.Body), `"u1"`) {
			t.Errorf("row body %q lacks u1", e.Body)
		}
	}
}

// TestEventsByKindField exercises jsonb ->> field extraction. The
// query projects body->>'user_id' as a text column.
func TestEventsByKindField(t *testing.T) {
	q, ctx, cleanup := setup(t)
	defer cleanup()
	seed(t, q, ctx)
	rows, err := q.EventsByKindField(ctx, db.EventsByKindFieldParams{Kind: "login", Limit: 10})
	if err != nil {
		t.Fatalf("EventsByKindField: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d login rows, want 2", len(rows))
	}
	// Order is created_at DESC, so u2 (later) before u1.
	if got, ok := rows[0].UserID.(string); !ok || got != "u2" {
		t.Errorf("rows[0].UserID = %v, want u2", rows[0].UserID)
	}
	if got, ok := rows[1].UserID.(string); !ok || got != "u1" {
		t.Errorf("rows[1].UserID = %v, want u1", rows[1].UserID)
	}
}

// TestErrorEvents exercises case-insensitive regex match (~*).
func TestErrorEvents(t *testing.T) {
	q, ctx, cleanup := setup(t)
	defer cleanup()
	seed(t, q, ctx)
	got, err := q.ErrorEvents(ctx)
	if err != nil {
		t.Fatalf("ErrorEvents: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d, want 1 (only the 'ERROR:' row matches)", len(got))
	}
	if !strings.Contains(got[0].Message, "ERROR") {
		t.Errorf("matched row body %q lacks ERROR", got[0].Message)
	}
}

// TestEventEpochs exercises extract(epoch FROM ts), cast to bigint
// to match pgmem-go's date_part return type. Each row's epoch should
// equal the seed timestamp's Unix epoch.
func TestEventEpochs(t *testing.T) {
	q, ctx, cleanup := setup(t)
	defer cleanup()
	seed(t, q, ctx)
	rows, err := q.EventEpochs(ctx)
	if err != nil {
		t.Fatalf("EventEpochs: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4", len(rows))
	}
	base := time.Date(2024, 7, 15, 9, 30, 0, 0, time.UTC).Unix()
	wantEpochs := []int64{base, base + 20*60, base + 2*3600, base + 3*3600}
	for i, r := range rows {
		if r.Epoch != wantEpochs[i] {
			t.Errorf("row %d: epoch %d, want %d", i, r.Epoch, wantEpochs[i])
		}
	}
}

// TestEventsPerHour exercises date_trunc('hour', ...) bucketing plus
// count() per bucket.
func TestEventsPerHour(t *testing.T) {
	q, ctx, cleanup := setup(t)
	defer cleanup()
	seed(t, q, ctx)
	got, err := q.EventsPerHour(ctx)
	if err != nil {
		t.Fatalf("EventsPerHour: %v", err)
	}
	// Buckets at 09:00 (two logins), 11:00 (error), 12:00 (purchase).
	expected := map[time.Time]int64{
		time.Date(2024, 7, 15, 9, 0, 0, 0, time.UTC):  2,
		time.Date(2024, 7, 15, 11, 0, 0, 0, time.UTC): 1,
		time.Date(2024, 7, 15, 12, 0, 0, 0, time.UTC): 1,
	}
	if len(got) != len(expected) {
		t.Fatalf("got %d buckets, want %d (%v)", len(got), len(expected), got)
	}
	for _, r := range got {
		want, ok := expected[r.Hour.UTC()]
		if !ok {
			t.Errorf("unexpected bucket %v", r.Hour)
			continue
		}
		if r.N != want {
			t.Errorf("bucket %v: got %d, want %d", r.Hour, r.N, want)
		}
	}
}
