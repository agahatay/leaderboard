package api

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// statusRecorder remembers the status code written by the wrapped handler.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// jsonRouteErrors makes ServeMux's own 404 and 405 answers use our JSON error
// format. ServeMux still decides the status code and the Allow header; only
// its plain-text body is replaced.
func jsonRouteErrors(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// An empty pattern means no route matched. ServeHTTP must still be
		// called (not the returned handler) so that r.PathValue works.
		if _, pattern := mux.Handler(r); pattern == "" {
			w = &routeErrorWriter{ResponseWriter: w}
		}
		mux.ServeHTTP(w, r)
	})
}

// routeErrorWriter turns the first WriteHeader call into a JSON error and
// drops the plain-text body that follows.
type routeErrorWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (w *routeErrorWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	writeError(w.ResponseWriter, code, strings.ToLower(http.StatusText(code)))
}

func (w *routeErrorWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusNotFound)
	}
	return len(b), nil // report success so the caller does not retry
}

// logRequests logs one line per request: method, path, status, duration.
func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		logger.LogAttrs(r.Context(), slog.LevelInfo, "http request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.status),
			slog.Duration("duration", time.Since(start)),
		)
	})
}
