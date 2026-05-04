// Package analytics_test exercises window functions, arrays + ANY,
// array_agg, and FROM-position unnest end-to-end against an embedded
// pgmem-go server.
package analytics_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaeawc/pgmem-go/examples/analytics/db"
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

// fixture seeds three players with several matches across two regions
// so the window-function tests have meaningful tie / partition shapes.
func fixture(t *testing.T, q *db.Queries, ctx context.Context) (a, b, c int64) {
	t.Helper()
	now := time.Date(2024, 7, 15, 12, 0, 0, 0, time.UTC)
	alice, err := q.AddPlayer(ctx, "alice")
	if err != nil {
		t.Fatalf("AddPlayer alice: %v", err)
	}
	bob, err := q.AddPlayer(ctx, "bob")
	if err != nil {
		t.Fatalf("AddPlayer bob: %v", err)
	}
	carol, err := q.AddPlayer(ctx, "carol")
	if err != nil {
		t.Fatalf("AddPlayer carol: %v", err)
	}
	// east region:
	//   alice 100, bob 90, alice 70
	// west region:
	//   carol 200, alice 200, bob 100
	for i, m := range []db.RecordMatchParams{
		{PlayerID: alice.ID, Region: "east", Score: 100, PlayedAt: now},
		{PlayerID: bob.ID, Region: "east", Score: 90, PlayedAt: now.Add(time.Minute)},
		{PlayerID: alice.ID, Region: "east", Score: 70, PlayedAt: now.Add(2 * time.Minute)},
		{PlayerID: carol.ID, Region: "west", Score: 200, PlayedAt: now.Add(3 * time.Minute)},
		{PlayerID: alice.ID, Region: "west", Score: 200, PlayedAt: now.Add(4 * time.Minute)},
		{PlayerID: bob.ID, Region: "west", Score: 100, PlayedAt: now.Add(5 * time.Minute)},
	} {
		if _, err := q.RecordMatch(ctx, m); err != nil {
			t.Fatalf("RecordMatch %d: %v", i, err)
		}
	}
	return alice.ID, bob.ID, carol.ID
}

// TestTopScoresByRegion exercises row_number() OVER (PARTITION BY
// region ORDER BY score DESC, id).
func TestTopScoresByRegion(t *testing.T) {
	q, ctx, cleanup := setup(t)
	defer cleanup()
	fixture(t, q, ctx)
	rows, err := q.TopScoresByRegion(ctx)
	if err != nil {
		t.Fatalf("TopScoresByRegion: %v", err)
	}
	// east has 3 matches, west has 3. Within each region rn 1..3.
	regionCounts := map[string]int{}
	for _, r := range rows {
		if r.Rn < 1 || r.Rn > 3 {
			t.Errorf("row %+v: rn out of range", r)
		}
		regionCounts[r.Region]++
	}
	if regionCounts["east"] != 3 || regionCounts["west"] != 3 {
		t.Errorf("region counts = %v, want 3 / 3", regionCounts)
	}
	// Within east, the top score must be 100; within west, 200.
	var east1, west1 int32
	for _, r := range rows {
		if r.Rn == 1 {
			if r.Region == "east" {
				east1 = r.Score
			}
			if r.Region == "west" {
				west1 = r.Score
			}
		}
	}
	if east1 != 100 || west1 != 200 {
		t.Errorf("rn=1 east=%d west=%d, want 100 / 200", east1, west1)
	}
}

// TestRankByScore exercises rank() OVER (ORDER BY score DESC). The
// 200/200 tie shares rank 1 and the next non-tied score is rank 3.
func TestRankByScore(t *testing.T) {
	q, ctx, cleanup := setup(t)
	defer cleanup()
	fixture(t, q, ctx)
	rows, err := q.RankByScore(ctx)
	if err != nil {
		t.Fatalf("RankByScore: %v", err)
	}
	// First two rows tied at rank 1 with score 200.
	if rows[0].Rk != 1 || rows[1].Rk != 1 {
		t.Errorf("first two ranks = %d / %d, want 1 / 1", rows[0].Rk, rows[1].Rk)
	}
	if rows[0].Score != 200 || rows[1].Score != 200 {
		t.Errorf("first two scores = %d / %d, want 200 / 200", rows[0].Score, rows[1].Score)
	}
	// Third row is the next non-tied score with rank 3 (rank-with-gap).
	if rows[2].Rk != 3 {
		t.Errorf("third rank = %d, want 3", rows[2].Rk)
	}
}

// TestScoresArrayPerPlayer exercises array_agg() per group.
func TestScoresArrayPerPlayer(t *testing.T) {
	q, ctx, cleanup := setup(t)
	defer cleanup()
	a, _, _ := fixture(t, q, ctx)
	rows, err := q.ScoresArrayPerPlayer(ctx)
	if err != nil {
		t.Fatalf("ScoresArrayPerPlayer: %v", err)
	}
	got := map[int64][]int64{}
	for _, r := range rows {
		got[r.PlayerID] = r.Scores
	}
	aliceScores := got[a]
	sort.Slice(aliceScores, func(i, j int) bool { return aliceScores[i] < aliceScores[j] })
	want := []int64{70, 100, 200}
	if len(aliceScores) != len(want) {
		t.Fatalf("alice scores: got %v, want %v", aliceScores, want)
	}
	for i := range want {
		if aliceScores[i] != want[i] {
			t.Errorf("alice[%d] = %d, want %d", i, aliceScores[i], want[i])
		}
	}
}

// TestPlayersByIDs exercises the "filter by id list" sqlc shape.
func TestPlayersByIDs(t *testing.T) {
	q, ctx, cleanup := setup(t)
	defer cleanup()
	a, _, c := fixture(t, q, ctx)
	got, err := q.PlayersByIDs(ctx, []int64{a, c})
	if err != nil {
		t.Fatalf("PlayersByIDs: %v", err)
	}
	if len(got) != 2 || got[0].Name != "alice" || got[1].Name != "carol" {
		t.Errorf("got %+v", got)
	}
}

// TestExpandIDs exercises FROM-position unnest. The result mirrors
// the input array.
func TestExpandIDs(t *testing.T) {
	q, ctx, cleanup := setup(t)
	defer cleanup()
	got, err := q.ExpandIDs(ctx, []int64{42, 7, 1024})
	if err != nil {
		t.Fatalf("ExpandIDs: %v", err)
	}
	want := []int64{7, 42, 1024}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}
