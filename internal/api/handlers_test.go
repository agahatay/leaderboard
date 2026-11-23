package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agahatay/leaderboard/internal/leaderboard"
)

func newTestHandler(t *testing.T) (http.Handler, *leaderboard.Service) {
	t.Helper()
	svc := leaderboard.NewService(leaderboard.NewMemoryStore())
	return NewHandler(svc, slog.New(slog.DiscardHandler)), svc
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// assertError checks status, content type and that the body is {"error": "..."}.
func assertError(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, wantStatus, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == "" {
		t.Errorf("body %q is not {\"error\": \"...\"} (err %v)", rec.Body, err)
	}
}

func TestSubmitScore(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		body       string
		wantStatus int
	}{
		{"valid", "/v1/leaderboards/global/scores", `{"player_id":"p1","score":42}`, http.StatusOK},
		{"zero score", "/v1/leaderboards/global/scores", `{"player_id":"p1","score":0}`, http.StatusOK},
		{"empty body", "/v1/leaderboards/global/scores", ``, http.StatusBadRequest},
		{"broken json", "/v1/leaderboards/global/scores", `{`, http.StatusBadRequest},
		{"unknown field", "/v1/leaderboards/global/scores", `{"player_id":"p1","score":1,"x":1}`, http.StatusBadRequest},
		{"trailing data", "/v1/leaderboards/global/scores", `{"player_id":"p1","score":1}{}`, http.StatusBadRequest},
		{"missing score", "/v1/leaderboards/global/scores", `{"player_id":"p1"}`, http.StatusBadRequest},
		{"missing player", "/v1/leaderboards/global/scores", `{"score":1}`, http.StatusBadRequest},
		{"float score", "/v1/leaderboards/global/scores", `{"player_id":"p1","score":1.5}`, http.StatusBadRequest},
		{"string score", "/v1/leaderboards/global/scores", `{"player_id":"p1","score":"1"}`, http.StatusBadRequest},
		{"negative score", "/v1/leaderboards/global/scores", `{"player_id":"p1","score":-1}`, http.StatusBadRequest},
		{"score too large", "/v1/leaderboards/global/scores", `{"player_id":"p1","score":1000000000001}`, http.StatusBadRequest},
		{"bad player id", "/v1/leaderboards/global/scores", `{"player_id":"a.b","score":1}`, http.StatusBadRequest},
		{"bad board id", "/v1/leaderboards/bad%20board/scores", `{"player_id":"p1","score":1}`, http.StatusBadRequest},
		{"body too large", "/v1/leaderboards/global/scores", `{"player_id":"` + strings.Repeat("a", 5000) + `","score":1}`, http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newTestHandler(t)
			rec := do(t, h, http.MethodPost, tt.path, tt.body)
			if tt.wantStatus != http.StatusOK {
				assertError(t, rec, tt.wantStatus)
				return
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
			}
			var got scoreResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.PlayerID != "p1" {
				t.Errorf("player_id = %q, want p1", got.PlayerID)
			}
		})
	}
}

// Unmatched routes must also answer with a JSON error body, not ServeMux's
// default plain text.
func TestRouteErrorsAreJSON(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantAllow  string
	}{
		{"unknown path", http.MethodGet, "/nope", http.StatusNotFound, ""},
		{"unknown version", http.MethodGet, "/v2/leaderboards/global/top", http.StatusNotFound, ""},
		{"GET on POST route", http.MethodGet, "/v1/leaderboards/global/scores", http.StatusMethodNotAllowed, "POST"},
		{"DELETE on PUT route", http.MethodDelete, "/v1/players/p1", http.StatusMethodNotAllowed, "PUT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newTestHandler(t)
			rec := do(t, h, tt.method, tt.path, "")
			assertError(t, rec, tt.wantStatus)
			if got := rec.Header().Get("Allow"); got != tt.wantAllow {
				t.Errorf("Allow = %q, want %q", got, tt.wantAllow)
			}
		})
	}
}

func TestTop(t *testing.T) {
	h, svc := newTestHandler(t)
	// p00..p11 with scores 0..110; p11 is the leader.
	for i := range 12 {
		if err := svc.SubmitScore(t.Context(), "global", fmt.Sprintf("p%02d", i), int64(i*10)); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantLen    int
	}{
		{"default limit is 10", "/v1/leaderboards/global/top", http.StatusOK, 10},
		{"limit 3", "/v1/leaderboards/global/top?limit=3", http.StatusOK, 3},
		{"limit above board size", "/v1/leaderboards/global/top?limit=100", http.StatusOK, 12},
		{"empty board", "/v1/leaderboards/empty/top", http.StatusOK, 0},
		{"limit 0", "/v1/leaderboards/global/top?limit=0", http.StatusBadRequest, 0},
		{"limit 101", "/v1/leaderboards/global/top?limit=101", http.StatusBadRequest, 0},
		{"limit not a number", "/v1/leaderboards/global/top?limit=abc", http.StatusBadRequest, 0},
		{"bad board id", "/v1/leaderboards/bad.board/top", http.StatusBadRequest, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, h, http.MethodGet, tt.path, "")
			if tt.wantStatus != http.StatusOK {
				assertError(t, rec, tt.wantStatus)
				return
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; body: %s", rec.Code, rec.Body)
			}
			// Decode into a raw map first to make sure "entries" is [] and not null.
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if string(raw["entries"]) == "null" {
				t.Fatal(`"entries" is null, want an array`)
			}
			var got topResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(got.Entries) != tt.wantLen {
				t.Fatalf("len = %d, want %d", len(got.Entries), tt.wantLen)
			}
			for i, e := range got.Entries {
				wantID := fmt.Sprintf("p%02d", 11-i)
				if e.Rank != int64(i+1) || e.PlayerID != wantID || e.Score != int64((11-i)*10) {
					t.Errorf("entry %d = %+v, want rank %d id %s", i, e, i+1, wantID)
				}
			}
		})
	}
}

func TestPlayerRank(t *testing.T) {
	h, svc := newTestHandler(t)
	for id, score := range map[string]int64{"alice": 100, "bob": 300} {
		if err := svc.SubmitScore(t.Context(), "global", id, score); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name       string
		path       string
		wantStatus int
		want       leaderboard.PlayerRank
	}{
		{"leader", "/v1/leaderboards/global/players/bob", http.StatusOK, leaderboard.PlayerRank{Rank: 1, Score: 300}},
		{"second", "/v1/leaderboards/global/players/alice", http.StatusOK, leaderboard.PlayerRank{Rank: 2, Score: 100}},
		{"not on board", "/v1/leaderboards/global/players/ghost", http.StatusNotFound, leaderboard.PlayerRank{}},
		{"unknown board", "/v1/leaderboards/other/players/bob", http.StatusNotFound, leaderboard.PlayerRank{}},
		{"bad player id", "/v1/leaderboards/global/players/a.b", http.StatusBadRequest, leaderboard.PlayerRank{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, h, http.MethodGet, tt.path, "")
			if tt.wantStatus != http.StatusOK {
				assertError(t, rec, tt.wantStatus)
				return
			}
			var got leaderboard.PlayerRank
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v (body %s)", err, rec.Body)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestSetNameShowsInTop(t *testing.T) {
	h, _ := newTestHandler(t)

	if rec := do(t, h, http.MethodPost, "/v1/leaderboards/global/scores", `{"player_id":"p1","score":5}`); rec.Code != http.StatusOK {
		t.Fatalf("submit: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, http.MethodPut, "/v1/players/p1", `{"name":"Ayşe"}`); rec.Code != http.StatusOK {
		t.Fatalf("set name: status %d, body %s", rec.Code, rec.Body)
	}

	rec := do(t, h, http.MethodGet, "/v1/leaderboards/global/top", "")
	var got topResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Name != "Ayşe" {
		t.Errorf("entries = %+v, want one entry named Ayşe", got.Entries)
	}
}

func TestSetNameValidation(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
	}{
		{"empty name", "/v1/players/p1", `{"name":""}`},
		{"blank name", "/v1/players/p1", `{"name":"   "}`},
		{"missing name", "/v1/players/p1", `{}`},
		{"name too long", "/v1/players/p1", `{"name":"` + strings.Repeat("x", 65) + `"}`},
		{"bad player id", "/v1/players/a.b", `{"name":"x"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newTestHandler(t)
			assertError(t, do(t, h, http.MethodPut, tt.path, tt.body), http.StatusBadRequest)
		})
	}
}

func TestHealthz(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := do(t, h, http.MethodGet, "/healthz", "")
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}
