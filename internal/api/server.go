// Package api exposes the leaderboard over HTTP.
package api

import (
	"log/slog"
	"net/http"

	"github.com/agahatay/leaderboard/internal/leaderboard"
)

type handler struct {
	svc    *leaderboard.Service
	logger *slog.Logger
}

// NewHandler returns the HTTP handler with all routes registered.
func NewHandler(svc *leaderboard.Service, logger *slog.Logger) http.Handler {
	h := &handler{svc: svc, logger: logger}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/leaderboards/{board}/scores", h.submitScore)
	mux.HandleFunc("GET /v1/leaderboards/{board}/top", h.top)
	mux.HandleFunc("GET /v1/leaderboards/{board}/players/{player}", h.playerRank)
	mux.HandleFunc("PUT /v1/players/{player}", h.setName)
	mux.HandleFunc("GET /healthz", h.healthz)

	return logRequests(logger, jsonRouteErrors(mux))
}
