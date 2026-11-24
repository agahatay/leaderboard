package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/agahatay/leaderboard/internal/ingest"
	"github.com/agahatay/leaderboard/internal/leaderboard"
)

// readyTimeout bounds the Redis PING behind /readyz.
const readyTimeout = 500 * time.Millisecond

type scoreRequest struct {
	PlayerID string `json:"player_id"`
	// Pointer so that a missing "score" can be told apart from 0.
	Score *int64 `json:"score"`
}

type scoreResponse struct {
	PlayerID string `json:"player_id"`
	Score    int64  `json:"score"`
}

type entriesResponse struct {
	Entries []leaderboard.Entry `json:"entries"`
}

type nameRequest struct {
	Name string `json:"name"`
}

type nameResponse struct {
	PlayerID string `json:"player_id"`
	Name     string `json:"name"`
}

func (h *handler) submitScore(w http.ResponseWriter, r *http.Request) {
	var req scoreRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Score == nil {
		writeError(w, http.StatusBadRequest, "score is required")
		return
	}
	ev := leaderboard.ScoreEvent{Board: r.PathValue("board"), PlayerID: req.PlayerID, Score: *req.Score}
	resp := scoreResponse{PlayerID: ev.PlayerID, Score: ev.Score}

	if h.queue == nil { // sync mode
		if err := h.svc.SubmitScore(r.Context(), ev.Board, ev.PlayerID, ev.Score); err != nil {
			h.writeServiceError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Async mode: validate now so bad input still gets a 400, then queue.
	if err := leaderboard.ValidateScoreEvent(ev); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	switch err := h.queue.Enqueue(ev); {
	case err == nil:
		writeJSON(w, http.StatusAccepted, resp)
	case errors.Is(err, ingest.ErrQueueFull):
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "write queue is full, retry later")
	case errors.Is(err, ingest.ErrClosed):
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "server is shutting down")
	default:
		h.writeServiceError(w, r, err)
	}
}

// intQuery reads an integer query parameter, or def if it is absent.
func intQuery(r *http.Request, name string, def int) (int, error) {
	s := r.URL.Query().Get(name)
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, &leaderboard.ValidationError{Field: name, Msg: "must be an integer"}
	}
	return n, nil
}

func (h *handler) top(w http.ResponseWriter, r *http.Request) {
	limit, err := intQuery(r, "limit", leaderboard.DefaultLimit)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	entries, err := h.svc.Top(r.Context(), r.PathValue("board"), limit)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entriesResponse{Entries: entries})
}

func (h *handler) playerRank(w http.ResponseWriter, r *http.Request) {
	pr, err := h.svc.PlayerRank(r.Context(), r.PathValue("board"), r.PathValue("player"))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pr)
}

func (h *handler) around(w http.ResponseWriter, r *http.Request) {
	radius, err := intQuery(r, "radius", leaderboard.DefaultRadius)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	entries, err := h.svc.Around(r.Context(), r.PathValue("board"), r.PathValue("player"), radius)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entriesResponse{Entries: entries})
}

func (h *handler) setName(w http.ResponseWriter, r *http.Request) {
	var req nameRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	player := r.PathValue("player")
	if err := h.svc.SetName(r.Context(), player, req.Name); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nameResponse{PlayerID: player, Name: req.Name})
}

// healthz is liveness: the process is up and serving HTTP. It also says
// which commit is running, so a deploy can be checked from outside.
func (h *handler) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "commit": h.commit})
}

// readyz is readiness: the store answers a PING within readyTimeout.
func (h *handler) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := h.ready.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "redis is not reachable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// writeServiceError maps domain errors to HTTP status codes. Unknown errors
// are logged and hidden from the client.
func (h *handler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	var verr *leaderboard.ValidationError
	switch {
	case errors.As(err, &verr):
		writeError(w, http.StatusBadRequest, verr.Error())
	case errors.Is(err, leaderboard.ErrNotFound):
		writeError(w, http.StatusNotFound, "player not found on this leaderboard")
	default:
		h.logger.ErrorContext(r.Context(), "request failed",
			"method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}
