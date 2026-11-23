package api

import (
	"net/http"
	"testing"
)

func TestAPIKey(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		key        string
		wantStatus int
	}{
		{"score without key", http.MethodPost, "/v1/leaderboards/g/scores", `{"player_id":"p","score":1}`, "", http.StatusUnauthorized},
		{"score with wrong key", http.MethodPost, "/v1/leaderboards/g/scores", `{"player_id":"p","score":1}`, "nope", http.StatusUnauthorized},
		{"score with key prefix", http.MethodPost, "/v1/leaderboards/g/scores", `{"player_id":"p","score":1}`, testAPIKey[:4], http.StatusUnauthorized},
		{"score with valid key", http.MethodPost, "/v1/leaderboards/g/scores", `{"player_id":"p","score":1}`, testAPIKey, http.StatusOK},
		{"name without key", http.MethodPut, "/v1/players/p", `{"name":"x"}`, "", http.StatusUnauthorized},
		{"name with valid key", http.MethodPut, "/v1/players/p", `{"name":"x"}`, testAPIKey, http.StatusOK},
		// Auth runs before the body is read, so a bad body without a key is still 401.
		{"bad body without key", http.MethodPost, "/v1/leaderboards/g/scores", `{`, "", http.StatusUnauthorized},
		// Reads are public.
		{"top without key", http.MethodGet, "/v1/leaderboards/g/top", "", "", http.StatusOK},
		{"healthz without key", http.MethodGet, "/healthz", "", "", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newTestHandler(t)
			rec := doWithKey(t, h, tt.method, tt.path, tt.body, tt.key)
			if tt.wantStatus == http.StatusUnauthorized {
				assertError(t, rec, tt.wantStatus)
				return
			}
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d; body %s", rec.Code, tt.wantStatus, rec.Body)
			}
		})
	}
}

// An empty configured key must never authorize anything, even an empty header.
func TestAPIKeyEmptyConfiguredKeyRejectsAll(t *testing.T) {
	h := requireAPIKey("")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, key := range []string{"", "anything"} {
		rec := doWithKey(t, h, http.MethodPost, "/", "", key)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("key %q: status %d, want 401", key, rec.Code)
		}
	}
}
