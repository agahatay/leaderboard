// Package api exposes the leaderboard over HTTP.
package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/agahatay/leaderboard/internal/leaderboard"
)

// ScoreQueue accepts score events for asynchronous writing. Enqueue must not
// block. *ingest.Ingester implements it.
type ScoreQueue interface {
	Enqueue(ev leaderboard.ScoreEvent) error
}

// Pinger reports whether the backing store is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Deps are the handler's dependencies.
type Deps struct {
	Service *leaderboard.Service
	// Queue switches POST scores to async mode (202). Nil means sync (200).
	Queue  ScoreQueue
	Ready  Pinger
	APIKey string
	Logger *slog.Logger
	// Commit is the git commit the binary was built from, shown by /healthz.
	Commit string
}

type handler struct {
	svc    *leaderboard.Service
	queue  ScoreQueue
	ready  Pinger
	logger *slog.Logger
	commit string
}

// NewHandler returns the HTTP handler with all routes registered.
func NewHandler(d Deps) http.Handler {
	h := &handler{svc: d.Service, queue: d.Queue, ready: d.Ready, logger: d.Logger, commit: d.Commit}
	auth := requireAPIKey(d.APIKey)

	mux := http.NewServeMux()
	mux.Handle("POST /v1/leaderboards/{board}/scores", auth(http.HandlerFunc(h.submitScore)))
	mux.HandleFunc("GET /v1/leaderboards/{board}/top", h.top)
	mux.HandleFunc("GET /v1/leaderboards/{board}/players/{player}", h.playerRank)
	mux.HandleFunc("GET /v1/leaderboards/{board}/players/{player}/around", h.around)
	mux.Handle("PUT /v1/players/{player}", auth(http.HandlerFunc(h.setName)))
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /readyz", h.readyz)

	return logRequests(d.Logger, jsonRouteErrors(mux))
}
