package leaderboard

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
)

func TestMemoryStoreSubmitScoreKeepsBest(t *testing.T) {
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			s := NewMemoryStore()
			for _, score := range tt.scores {
				if err := s.SubmitScore(ctx, "board", "p1", score); err != nil {
					t.Fatalf("SubmitScore(%d): %v", score, err)
				}
			}
			got, err := s.PlayerRank(ctx, "board", "p1")
			if err != nil {
				t.Fatalf("PlayerRank: %v", err)
			}
			if got.Score != tt.want || got.Rank != 1 {
				t.Errorf("got %+v, want score %d rank 1", got, tt.want)
			}
		})
	}
}

// seed fills a board with a fixed set of scores, including a tie.
func seed(t *testing.T, s *MemoryStore, board string) {
	t.Helper()
	scores := map[string]int64{"alice": 100, "bob": 300, "carol": 200, "dave": 200, "erin": 50}
	for id, score := range scores {
		if err := s.SubmitScore(t.Context(), board, id, score); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
}

func TestMemoryStoreTop(t *testing.T) {
	s := NewMemoryStore()
	seed(t, s, "board")
	if err := s.SetName(t.Context(), "bob", "Bob"); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		board   string
		n       int
		wantIDs []string
	}{
		// dave and carol tie at 200; dave > carol, so dave comes first.
		{"top 3 with tie", "board", 3, []string{"bob", "dave", "carol"}},
		{"n larger than board", "board", 10, []string{"bob", "dave", "carol", "alice", "erin"}},
		{"top 1", "board", 1, []string{"bob"}},
		{"unknown board", "nope", 10, []string{}},
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
			ids := make([]string, len(got))
			for i, e := range got {
				ids[i] = e.PlayerID
				if e.Rank != int64(i+1) {
					t.Errorf("entry %d (%s): rank %d, want %d", i, e.PlayerID, e.Rank, i+1)
				}
				// Top and PlayerRank must agree on ordering.
				pr, err := s.PlayerRank(t.Context(), tt.board, e.PlayerID)
				if err != nil || pr.Rank != e.Rank || pr.Score != e.Score {
					t.Errorf("PlayerRank(%s) = %+v, %v; Top says rank %d score %d", e.PlayerID, pr, err, e.Rank, e.Score)
				}
			}
			if !slices.Equal(ids, tt.wantIDs) {
				t.Errorf("ids = %v, want %v", ids, tt.wantIDs)
			}
		})
	}

	t.Run("names are filled in", func(t *testing.T) {
		got, err := s.Top(t.Context(), "board", 2)
		if err != nil {
			t.Fatal(err)
		}
		if got[0].Name != "Bob" || got[1].Name != "" {
			t.Errorf("names = %q, %q; want \"Bob\", \"\"", got[0].Name, got[1].Name)
		}
	})
}

func TestMemoryStorePlayerRank(t *testing.T) {
	s := NewMemoryStore()
	seed(t, s, "board")

	tests := []struct {
		name    string
		board   string
		player  string
		want    PlayerRank
		wantErr error
	}{
		{"leader", "board", "bob", PlayerRank{Rank: 1, Score: 300}, nil},
		{"tie, higher id first", "board", "dave", PlayerRank{Rank: 2, Score: 200}, nil},
		{"tie, lower id second", "board", "carol", PlayerRank{Rank: 3, Score: 200}, nil},
		{"last", "board", "erin", PlayerRank{Rank: 5, Score: 50}, nil},
		{"unknown player", "board", "zed", PlayerRank{}, ErrNotFound},
		{"unknown board", "nope", "bob", PlayerRank{}, ErrNotFound},
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

func TestMemoryStoreBoardsAreIndependent(t *testing.T) {
	ctx := t.Context()
	s := NewMemoryStore()
	if err := s.SubmitScore(ctx, "a", "p1", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PlayerRank(ctx, "b", "p1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("player leaked into another board: err = %v", err)
	}
}

// Run with -race: concurrent writers and readers must not trigger the detector.
func TestMemoryStoreConcurrentAccess(t *testing.T) {
	ctx := t.Context()
	s := NewMemoryStore()
	var wg sync.WaitGroup
	for g := range 20 {
		wg.Go(func() {
			player := fmt.Sprintf("p%d", g)
			for i := range 100 {
				if err := s.SubmitScore(ctx, "board", player, int64(i)); err != nil {
					t.Errorf("SubmitScore: %v", err)
					return
				}
				if _, err := s.Top(ctx, "board", 5); err != nil {
					t.Errorf("Top: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()

	top, err := s.Top(ctx, "board", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 20 {
		t.Fatalf("len(top) = %d, want 20", len(top))
	}
	for _, e := range top {
		if e.Score != 99 {
			t.Errorf("%s score = %d, want 99", e.PlayerID, e.Score)
		}
	}
}
