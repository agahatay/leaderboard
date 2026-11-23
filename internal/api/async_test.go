package api

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/agahatay/leaderboard/internal/ingest"
	"github.com/agahatay/leaderboard/internal/leaderboard"
)

// fakeQueue records events, or returns err without recording.
type fakeQueue struct {
	err    error
	events []leaderboard.ScoreEvent
}

func (q *fakeQueue) Enqueue(ev leaderboard.ScoreEvent) error {
	if q.err != nil {
		return q.err
	}
	q.events = append(q.events, ev)
	return nil
}

func newAsyncHandler(q ScoreQueue) http.Handler {
	return NewHandler(Deps{
		Service: leaderboard.NewService(leaderboard.NewMemoryStore()),
		Queue:   q, Ready: okPinger{}, APIKey: testAPIKey, Logger: slog.New(slog.DiscardHandler),
	})
}

func TestAsyncSubmitScore(t *testing.T) {
	tests := []struct {
		name           string
		queueErr       error
		body           string
		wantStatus     int
		wantRetryAfter string
		wantQueued     int
	}{
		{"accepted", nil, `{"player_id":"p1","score":42}`, http.StatusAccepted, "", 1},
		{"queue full", ingest.ErrQueueFull, `{"player_id":"p1","score":42}`, http.StatusServiceUnavailable, "1", 0},
		{"shutting down", ingest.ErrClosed, `{"player_id":"p1","score":42}`, http.StatusServiceUnavailable, "1", 0},
		{"unexpected queue error", errors.New("boom"), `{"player_id":"p1","score":42}`, http.StatusInternalServerError, "", 0},
		// Validation happens before queueing: bad input is never queued.
		{"invalid score not queued", nil, `{"player_id":"p1","score":-1}`, http.StatusBadRequest, "", 0},
		{"invalid player not queued", nil, `{"player_id":"a.b","score":1}`, http.StatusBadRequest, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &fakeQueue{err: tt.queueErr}
			rec := do(t, newAsyncHandler(q), http.MethodPost, "/v1/leaderboards/global/scores", tt.body)

			if tt.wantStatus == http.StatusAccepted {
				if rec.Code != tt.wantStatus {
					t.Fatalf("status = %d, want 202; body %s", rec.Code, rec.Body)
				}
			} else {
				assertError(t, rec, tt.wantStatus)
			}
			if got := rec.Header().Get("Retry-After"); got != tt.wantRetryAfter {
				t.Errorf("Retry-After = %q, want %q", got, tt.wantRetryAfter)
			}
			if len(q.events) != tt.wantQueued {
				t.Fatalf("queued %d events, want %d", len(q.events), tt.wantQueued)
			}
			if tt.wantQueued == 1 {
				want := leaderboard.ScoreEvent{Board: "global", PlayerID: "p1", Score: 42}
				if q.events[0] != want {
					t.Errorf("queued %+v, want %+v", q.events[0], want)
				}
			}
		})
	}
}

// With a real Ingester whose single worker is stuck, the HTTP layer must
// answer 503 + Retry-After once the queue is full.
func TestAsyncSubmitScoreFullRealQueue(t *testing.T) {
	block := make(chan struct{})
	w := &blockingWriter{block: block, started: make(chan struct{}, 1)}
	ing, err := ingest.New(w, ingest.Config{QueueSize: 1, Workers: 1, BatchSize: 1, FlushInterval: time.Hour}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		close(block)
		if err := ing.Close(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	h := newAsyncHandler(ing)

	post := func() *httptest.ResponseRecorder {
		return do(t, h, http.MethodPost, "/v1/leaderboards/g/scores", `{"player_id":"p","score":1}`)
	}
	// 1st request: taken off the queue by the worker, which then blocks.
	if rec := post(); rec.Code != http.StatusAccepted {
		t.Fatalf("1st: %d", rec.Code)
	}
	<-w.started
	// 2nd request: fills the single queue slot.
	if rec := post(); rec.Code != http.StatusAccepted {
		t.Fatalf("2nd: %d", rec.Code)
	}
	rec := post()
	assertError(t, rec, http.StatusServiceUnavailable)
	if rec.Header().Get("Retry-After") != "1" {
		t.Errorf("Retry-After = %q, want 1", rec.Header().Get("Retry-After"))
	}
}
