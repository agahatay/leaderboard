package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckHealth(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{"healthy", http.StatusOK, false},
		{"unhealthy", http.StatusServiceUnavailable, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			err := checkHealth(t.Context(), srv.URL+"/healthz")
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}

	t.Run("nothing listening", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close() // the port is now closed
		if err := checkHealth(t.Context(), url); err == nil {
			t.Error("expected an error")
		}
	})
}
