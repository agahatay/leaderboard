package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/agahatay/leaderboard/internal/leaderboard"
)

// blockingWriter is an ingest.BatchWriter that waits until block is closed.
type blockingWriter struct {
	block   chan struct{}
	started chan struct{}
}

func (w *blockingWriter) SubmitScores(context.Context, []leaderboard.ScoreEvent) error {
	select {
	case w.started <- struct{}{}:
	default:
	}
	<-w.block
	return nil
}

type pingerFunc func(ctx context.Context) error

func (f pingerFunc) Ping(ctx context.Context) error { return f(ctx) }

func TestReadyz(t *testing.T) {
	tests := []struct {
		name       string
		ping       pingerFunc
		wantStatus int
	}{
		{"redis up", func(context.Context) error { return nil }, http.StatusOK},
		{"redis down", func(context.Context) error { return errors.New("connection refused") }, http.StatusServiceUnavailable},
		{"redis hangs", func(ctx context.Context) error {
			// Must be given a deadline of at most 500ms, then give up.
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 500*time.Millisecond {
				return errors.New("no 500ms deadline on the ping context")
			}
			<-ctx.Done()
			return ctx.Err()
		}, http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(Deps{
				Service: leaderboard.NewService(leaderboard.NewMemoryStore()),
				Ready:   tt.ping, APIKey: testAPIKey, Logger: slog.New(slog.DiscardHandler),
			})
			start := time.Now()
			rec := doWithKey(t, h, http.MethodGet, "/readyz", "", "")
			if tt.wantStatus != http.StatusOK {
				assertError(t, rec, tt.wantStatus)
			} else if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if d := time.Since(start); d > 2*time.Second {
				t.Errorf("readyz took %v", d)
			}
		})
	}
}
