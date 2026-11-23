// Package storetest holds behaviour tests that every leaderboard.Store
// implementation must pass. Call Run from the implementation's own test file.
package storetest

import (
	"errors"
	"fmt"
	"hash/fnv"
	"slices"
	"sync"
	"testing"

	"github.com/agahatay/leaderboard/internal/leaderboard"
)

// Factory returns a fresh Store for one test. It may register t.Cleanup to
// remove the keys starting with Prefix(t).
type Factory func(t *testing.T) leaderboard.Store

// Prefix returns a short, valid ID unique to the test t. Every board and
// player ID a test uses starts with it, so tests sharing one Redis do not
// collide and can clean up after themselves.
func Prefix(t *testing.T) string {
	h := fnv.New64a()
	h.Write([]byte(t.Name()))
	return fmt.Sprintf("t%016x", h.Sum64())
}

// Run executes the whole suite against stores built by newStore.
func Run(t *testing.T, newStore Factory) {
	t.Run("SubmitScoreKeepsBest", func(t *testing.T) { testSubmitScoreKeepsBest(t, newStore) })
	t.Run("Top", func(t *testing.T) { testTop(t, newStore) })
	t.Run("PlayerRank", func(t *testing.T) { testPlayerRank(t, newStore) })
	t.Run("SubmitScoresBatch", func(t *testing.T) { testSubmitScoresBatch(t, newStore) })
	t.Run("Around", func(t *testing.T) { testAround(t, newStore) })
	t.Run("BoardsAreIndependent", func(t *testing.T) { testBoardsAreIndependent(t, newStore) })
	t.Run("ConcurrentAccess", func(t *testing.T) { testConcurrentAccess(t, newStore) })
}

func testSubmitScoreKeepsBest(t *testing.T, newStore Factory) {
	s, p := newStore(t), Prefix(t)
	tests := []struct {
		name   string
		scores []int64
		want   int64
	}{
		{"first score is stored", []int64{50}, 50},
		{"higher score replaces", []int64{50, 80}, 80},
		{"lower score is ignored", []int64{80, 50}, 80},
		{"equal score keeps value", []int64{80, 80}, 80},
		{"zero is a valid score", []int64{0}, 0},
		{"max score is exact", []int64{leaderboard.MaxScore - 1, leaderboard.MaxScore}, leaderboard.MaxScore},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			board := fmt.Sprintf("%s-%d", p, i) // one board per case
			for _, score := range tt.scores {
				if err := s.SubmitScore(t.Context(), board, p+"_p1", score); err != nil {
					t.Fatalf("SubmitScore(%d): %v", score, err)
				}
			}
			got, err := s.PlayerRank(t.Context(), board, p+"_p1")
			if err != nil {
				t.Fatalf("PlayerRank: %v", err)
			}
			if got.Score != tt.want || got.Rank != 1 {
				t.Errorf("got %+v, want score %d rank 1", got, tt.want)
			}
		})
	}
}

// seed fills board with a fixed set of scores, including a tie at 200.
func seed(t *testing.T, s leaderboard.Store, board, p string) {
	t.Helper()
	scores := map[string]int64{"alice": 100, "bob": 300, "carol": 200, "dave": 200, "erin": 50}
	for id, score := range scores {
		if err := s.SubmitScore(t.Context(), board, p+"_"+id, score); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
}

// ids adds the test prefix to bare player names.
func ids(p string, names ...string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = p + "_" + n
	}
	return out
}

func testTop(t *testing.T, newStore Factory) {
	s, p := newStore(t), Prefix(t)
	seed(t, s, p, p)
	if err := s.SetName(t.Context(), p+"_bob", "Bob"); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		board   string
		n       int
		wantIDs []string
	}{
		// dave and carol tie at 200; "dave" > "carol", so dave comes first.
		{"top 3 with tie", p, 3, ids(p, "bob", "dave", "carol")},
		{"n larger than board", p, 10, ids(p, "bob", "dave", "carol", "alice", "erin")},
		{"top 1", p, 1, ids(p, "bob")},
		{"unknown board", p + "-none", 10, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.Top(t.Context(), tt.board, tt.n)
			if err != nil {
				t.Fatalf("Top: %v", err)
			}
			if got == nil {
				t.Fatal("Top returned nil slice, want non-nil")
			}
			gotIDs := make([]string, len(got))
			for i, e := range got {
				gotIDs[i] = e.PlayerID
				if e.Rank != int64(i+1) {
					t.Errorf("entry %d (%s): rank %d, want %d", i, e.PlayerID, e.Rank, i+1)
				}
				// Top and PlayerRank must agree on ordering.
				pr, err := s.PlayerRank(t.Context(), tt.board, e.PlayerID)
				if err != nil || pr.Rank != e.Rank || pr.Score != e.Score {
					t.Errorf("PlayerRank(%s) = %+v, %v; Top says rank %d score %d", e.PlayerID, pr, err, e.Rank, e.Score)
				}
			}
			if !slices.Equal(gotIDs, tt.wantIDs) {
				t.Errorf("ids = %v, want %v", gotIDs, tt.wantIDs)
			}
		})
	}

	t.Run("names are filled in", func(t *testing.T) {
		got, err := s.Top(t.Context(), p, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].Name != "Bob" || got[1].Name != "" {
			t.Errorf("got %+v, want names \"Bob\" and \"\"", got)
		}
	})
}

func testPlayerRank(t *testing.T, newStore Factory) {
	s, p := newStore(t), Prefix(t)
	seed(t, s, p, p)

	tests := []struct {
		name    string
		board   string
		player  string
		want    leaderboard.PlayerRank
		wantErr error
	}{
		{"leader", p, p + "_bob", leaderboard.PlayerRank{Rank: 1, Score: 300}, nil},
		{"tie, higher id first", p, p + "_dave", leaderboard.PlayerRank{Rank: 2, Score: 200}, nil},
		{"tie, lower id second", p, p + "_carol", leaderboard.PlayerRank{Rank: 3, Score: 200}, nil},
		{"last", p, p + "_erin", leaderboard.PlayerRank{Rank: 5, Score: 50}, nil},
		{"unknown player", p, p + "_zed", leaderboard.PlayerRank{}, leaderboard.ErrNotFound},
		{"unknown board", p + "-none", p + "_bob", leaderboard.PlayerRank{}, leaderboard.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.PlayerRank(t.Context(), tt.board, tt.player)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func testSubmitScoresBatch(t *testing.T, newStore Factory) {
	s, p := newStore(t), Prefix(t)
	boardA, boardB := p+"-a", p+"-b"
	if err := s.SubmitScore(t.Context(), boardA, p+"_old", 500); err != nil {
		t.Fatal(err)
	}
	batch := []leaderboard.ScoreEvent{
		{Board: boardA, PlayerID: p + "_x", Score: 10},
		{Board: boardA, PlayerID: p + "_x", Score: 30}, // same player twice: best wins
		{Board: boardA, PlayerID: p + "_x", Score: 20},
		{Board: boardA, PlayerID: p + "_old", Score: 100}, // lower than stored 500
		{Board: boardB, PlayerID: p + "_x", Score: 7},     // other board in same batch
	}
	if err := s.SubmitScores(t.Context(), batch); err != nil {
		t.Fatalf("SubmitScores: %v", err)
	}
	if err := s.SubmitScores(t.Context(), nil); err != nil {
		t.Fatalf("SubmitScores(nil): %v", err)
	}

	tests := []struct {
		board, player string
		want          int64
	}{
		{boardA, p + "_x", 30},
		{boardA, p + "_old", 500},
		{boardB, p + "_x", 7},
	}
	for _, tt := range tests {
		got, err := s.PlayerRank(t.Context(), tt.board, tt.player)
		if err != nil {
			t.Errorf("PlayerRank(%s, %s): %v", tt.board, tt.player, err)
			continue
		}
		if got.Score != tt.want {
			t.Errorf("%s on %s: score %d, want %d", tt.player, tt.board, got.Score, tt.want)
		}
	}
}

func testAround(t *testing.T, newStore Factory) {
	s, p := newStore(t), Prefix(t)
	seed(t, s, p, p) // order: bob, dave, carol, alice, erin
	if err := s.SetName(t.Context(), p+"_carol", "Carol"); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		board     string
		player    string
		radius    int
		wantIDs   []string
		wantFirst int64 // rank of the first entry
		wantErr   error
	}{
		{"middle", p, p + "_carol", 1, ids(p, "dave", "carol", "alice"), 2, nil},
		{"clamped at top", p, p + "_bob", 2, ids(p, "bob", "dave", "carol"), 1, nil},
		{"clamped at bottom", p, p + "_erin", 2, ids(p, "carol", "alice", "erin"), 3, nil},
		{"radius covers board", p, p + "_carol", 50, ids(p, "bob", "dave", "carol", "alice", "erin"), 1, nil},
		{"unknown player", p, p + "_zed", 1, nil, 0, leaderboard.ErrNotFound},
		{"unknown board", p + "-none", p + "_bob", 1, nil, 0, leaderboard.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.Around(t.Context(), tt.board, tt.player, tt.radius)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			gotIDs := make([]string, len(got))
			for i, e := range got {
				gotIDs[i] = e.PlayerID
				if want := tt.wantFirst + int64(i); e.Rank != want {
					t.Errorf("%s: rank %d, want %d", e.PlayerID, e.Rank, want)
				}
				if e.PlayerID == p+"_carol" && e.Name != "Carol" {
					t.Errorf("carol's name = %q, want Carol", e.Name)
				}
			}
			if !slices.Equal(gotIDs, tt.wantIDs) {
				t.Errorf("ids = %v, want %v", gotIDs, tt.wantIDs)
			}
		})
	}
}

func testBoardsAreIndependent(t *testing.T, newStore Factory) {
	s, p := newStore(t), Prefix(t)
	if err := s.SubmitScore(t.Context(), p+"-a", p+"_p1", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PlayerRank(t.Context(), p+"-b", p+"_p1"); !errors.Is(err, leaderboard.ErrNotFound) {
		t.Errorf("player leaked into another board: err = %v", err)
	}
}

// Run with -race: concurrent writers and readers must not trigger the detector.
func testConcurrentAccess(t *testing.T, newStore Factory) {
	s, p := newStore(t), Prefix(t)
	const writers, rounds = 20, 50
	var wg sync.WaitGroup
	for g := range writers {
		wg.Go(func() {
			player := fmt.Sprintf("%s_p%02d", p, g)
			for i := range rounds {
				if err := s.SubmitScore(t.Context(), p, player, int64(i)); err != nil {
					t.Errorf("SubmitScore: %v", err)
					return
				}
				if _, err := s.Top(t.Context(), p, 5); err != nil {
					t.Errorf("Top: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()

	top, err := s.Top(t.Context(), p, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != writers {
		t.Fatalf("len(top) = %d, want %d", len(top), writers)
	}
	for _, e := range top {
		if e.Score != rounds-1 {
			t.Errorf("%s score = %d, want %d", e.PlayerID, e.Score, rounds-1)
		}
	}
}
