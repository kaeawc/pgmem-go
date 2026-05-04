package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/postgres"
)

func jsonbPool(t *testing.T) (*pgxpool.Pool, context.Context, func()) {
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

// TestJSONB_RoundTrip: store a JSON object and read it back via pgx's
// json-decoder destination.
func TestJSONB_RoundTrip(t *testing.T) {
	pool, ctx, cleanup := jsonbPool(t)
	defer cleanup()

	if _, err := pool.Exec(ctx, `CREATE TABLE docs (id int PRIMARY KEY, body jsonb NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	type doc struct {
		Title string `json:"title"`
		Tags  []int  `json:"tags"`
	}
	want := doc{Title: "first", Tags: []int{1, 2, 3}}
	wantBytes, _ := json.Marshal(want)
	if _, err := pool.Exec(ctx, `INSERT INTO docs (id, body) VALUES ($1, $2)`, int32(1), wantBytes); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	var got doc
	if err := pool.QueryRow(ctx, `SELECT body FROM docs WHERE id = $1`, int32(1)).Scan(&got); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if got.Title != want.Title || len(got.Tags) != len(want.Tags) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestJSONB_UniqueConstraint exercises the same []byte-keyed unique
// path bytea uses; jsonb stores the bytes directly so two equal
// payloads must collide and distinct payloads must coexist.
func TestJSONB_UniqueConstraint(t *testing.T) {
	pool, ctx, cleanup := jsonbPool(t)
	defer cleanup()

	if _, err := pool.Exec(ctx, `CREATE TABLE keyed (k jsonb PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	first := []byte(`{"a":1}`)
	if _, err := pool.Exec(ctx, `INSERT INTO keyed (k) VALUES ($1)`, first); err != nil {
		t.Fatalf("first INSERT: %v", err)
	}
	_, err := pool.Exec(ctx, `INSERT INTO keyed (k) VALUES ($1)`, first)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Errorf("dup: got %v, want 23505", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO keyed (k) VALUES ($1)`, []byte(`{"a":2}`)); err != nil {
		t.Errorf("distinct: got %v, want success", err)
	}
}

// TestJSONB_AcceptsScalarAndArray covers non-object roots — PG jsonb
// accepts any valid JSON value, not just objects.
func TestJSONB_AcceptsScalarAndArray(t *testing.T) {
	pool, ctx, cleanup := jsonbPool(t)
	defer cleanup()
	if _, err := pool.Exec(ctx, `CREATE TABLE blobs (id int PRIMARY KEY, body jsonb NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	for i, body := range [][]byte{
		[]byte(`123`),
		[]byte(`"a string"`),
		[]byte(`[1, 2, 3]`),
		[]byte(`true`),
		[]byte(`null`),
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO blobs (id, body) VALUES ($1, $2)`, int32(i+1), body); err != nil {
			t.Errorf("body %d (%s): %v", i, body, err)
		}
	}
}
