package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/agahatay/leaderboard/internal/leaderboard"
)

type scoreRequest struct {
	PlayerID string `json:"player_id"`
	// Pointer so that a missing "score" can be told apart from 0.
	Score *int64 `json:"score"`
}

type scoreResponse struct {
	PlayerID string `json:"player_id"`
	Score    int64  `json:"score"`
}

type topResponse struct {
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
	if err := h.svc.SubmitScore(r.Context(), r.PathValue("board"), req.PlayerID, *req.Score); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, scoreResponse{PlayerID: req.PlayerID, Score: *req.Score})
}

func (h *handler) top(w http.ResponseWriter, r *http.Request) {
	limit := leaderboard.DefaultLimit
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "limit must be an integer")
			return
		}
		limit = n
	}
	entries, err := h.svc.Top(r.Context(), r.PathValue("board"), limit)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, topResponse{Entries: entries})
}

func (h *handler) playerRank(w http.ResponseWriter, r *http.Request) {
	pr, err := h.svc.PlayerRank(r.Context(), r.PathValue("board"), r.PathValue("player"))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pr)
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

func (h *handler) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
