package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// maxBodyBytes caps request bodies. Our payloads are tiny.
const maxBodyBytes = 4 << 10 // 4 KiB

type errorResponse struct {
	Error string `json:"error"`
}

// writeJSON writes v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status is already sent; an encode error here means the client went
	// away, and there is nothing useful left to tell it.
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes {"error": msg} with the given status code.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

// decodeJSON reads exactly one JSON object from the body into dst. Unknown
// fields, trailing data and oversized bodies are rejected. On failure it
// writes the error response itself and returns false.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	err := dec.Decode(dst)
	if err == nil {
		// A second Decode must hit EOF, otherwise there is trailing data.
		if err2 := dec.Decode(&struct{}{}); !errors.Is(err2, io.EOF) {
			writeError(w, http.StatusBadRequest, "request body must contain a single JSON object")
			return false
		}
		return true
	}

	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
	case errors.Is(err, io.EOF):
		writeError(w, http.StatusBadRequest, "request body is empty")
	default:
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
	}
	return false
}
