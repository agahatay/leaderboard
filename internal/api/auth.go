package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
)

// requireAPIKey returns middleware that lets a request through only if its
// X-API-Key header equals key.
//
// Both values are hashed first so the comparison always handles 32 bytes:
// subtle.ConstantTimeCompare returns early on a length mismatch, which would
// leak the key's length through timing.
func requireAPIKey(key string) func(http.Handler) http.Handler {
	want := sha256.Sum256([]byte(key))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := r.Header.Get("X-API-Key")
			gotSum := sha256.Sum256([]byte(got))
			if key == "" || got == "" || subtle.ConstantTimeCompare(gotSum[:], want[:]) != 1 {
				writeError(w, http.StatusUnauthorized, "missing or invalid X-API-Key header")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
