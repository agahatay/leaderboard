package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
)

func TestAround(t *testing.T) {
	h, svc := newTestHandler(t)
	// p00..p19 with scores 0..190; rank 1 is p19, p10 is rank 10, p00 is rank 20.
	for i := range 20 {
		if err := svc.SubmitScore(t.Context(), "global", fmt.Sprintf("p%02d", i), int64(i*10)); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantRanks  []int64
	}{
		{"default radius 5", "/v1/leaderboards/global/players/p10/around", http.StatusOK, []int64{5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}},
		{"radius 1", "/v1/leaderboards/global/players/p10/around?radius=1", http.StatusOK, []int64{9, 10, 11}},
		{"clamped at top", "/v1/leaderboards/global/players/p19/around?radius=2", http.StatusOK, []int64{1, 2, 3}},
		{"clamped at bottom", "/v1/leaderboards/global/players/p00/around?radius=2", http.StatusOK, []int64{18, 19, 20}},
		{"radius 0", "/v1/leaderboards/global/players/p10/around?radius=0", http.StatusBadRequest, nil},
		{"radius 51", "/v1/leaderboards/global/players/p10/around?radius=51", http.StatusBadRequest, nil},
		{"radius not a number", "/v1/leaderboards/global/players/p10/around?radius=x", http.StatusBadRequest, nil},
		{"unknown player", "/v1/leaderboards/global/players/ghost/around", http.StatusNotFound, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, h, http.MethodGet, tt.path, "")
			if tt.wantStatus != http.StatusOK {
				assertError(t, rec, tt.wantStatus)
				return
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; body %s", rec.Code, rec.Body)
			}
			var got entriesResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			ranks := make([]int64, len(got.Entries))
			for i, e := range got.Entries {
				ranks[i] = e.Rank
			}
			if !slices.Equal(ranks, tt.wantRanks) {
				t.Errorf("ranks = %v, want %v", ranks, tt.wantRanks)
			}
		})
	}
}
