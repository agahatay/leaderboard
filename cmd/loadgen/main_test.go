package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestPercentile(t *testing.T) {
	ms := func(ns ...int) []time.Duration {
		out := make([]time.Duration, len(ns))
		for i, n := range ns {
			out[i] = time.Duration(n) * time.Millisecond
		}
		return out
	}
	hundred := make([]int, 100)
	for i := range hundred {
		hundred[i] = i + 1
	}

	tests := []struct {
		name   string
		sorted []time.Duration
		p      float64
		want   time.Duration
	}{
		{"empty", nil, 50, 0},
		{"single", ms(7), 99, 7 * time.Millisecond},
		{"p50 of 1..100", ms(hundred...), 50, 50 * time.Millisecond},
		{"p95 of 1..100", ms(hundred...), 95, 95 * time.Millisecond},
		{"p99 of 1..100", ms(hundred...), 99, 99 * time.Millisecond},
		{"max of 1..100", ms(hundred...), 100, 100 * time.Millisecond},
		{"p50 of 4 rounds up", ms(1, 2, 3, 4), 50, 2 * time.Millisecond},
		{"p51 of 4", ms(1, 2, 3, 4), 51, 3 * time.Millisecond},
		{"tiny p is first", ms(1, 2, 3), 0.1, 1 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := percentile(tt.sorted, tt.p); got != tt.want {
				t.Errorf("percentile(p=%v) = %s, want %s", tt.p, got, tt.want)
			}
		})
	}
}

func TestCollect(t *testing.T) {
	ch := make(chan result, 10)
	ch <- result{Latency: 3 * time.Millisecond, Status: 202}
	ch <- result{Latency: 1 * time.Millisecond, Status: 200}
	ch <- result{Latency: 9 * time.Millisecond, Status: 503}
	ch <- result{Latency: 2 * time.Millisecond, Err: fmt.Errorf("dial: %w", errConnRefused)}
	ch <- result{Latency: 5 * time.Second, Err: context.DeadlineExceeded}
	close(ch)

	s := collect(ch)
	if s.Total != 5 || s.Success != 2 {
		t.Errorf("total=%d success=%d, want 5 and 2", s.Total, s.Success)
	}
	if s.Statuses[503] != 1 || s.Statuses[200] != 1 || s.Statuses[202] != 1 {
		t.Errorf("statuses = %v", s.Statuses)
	}
	if s.NetErrors["connection refused"] != 1 || s.NetErrors["timeout"] != 1 {
		t.Errorf("net errors = %v", s.NetErrors)
	}
	// Only 2xx latencies, sorted.
	if len(s.Latencies) != 2 || s.Latencies[0] != time.Millisecond || s.Latencies[1] != 3*time.Millisecond {
		t.Errorf("latencies = %v", s.Latencies)
	}
}

func TestParseConfig(t *testing.T) {
	env := func(key string) func(string) string {
		return func(string) string { return key }
	}
	tests := []struct {
		name    string
		args    []string
		key     string
		wantErr string
	}{
		{"defaults", nil, "k", ""},
		{"no api key", nil, "", "API_KEY"},
		{"zero clients", []string{"-c", "0"}, "k", "-c"},
		{"zero duration", []string{"-d", "0s"}, "k", "-d"},
		{"negative rate", []string{"-rate", "-1"}, "k", "-rate"},
		{"negative players", []string{"-players", "-1"}, "k", "-players"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseConfig(tt.args, env(tt.key))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

// TestRun drives run against a fake service: every request the server saw
// must be counted exactly once, with the right status.
func TestRun(t *testing.T) {
	var (
		served   atomic.Int64
		rejected atomic.Int64
		mu       sync.Mutex
		players  = map[string]bool{}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/leaderboards/b1/scores" {
			http.Error(w, "bad route "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			return
		}
		if r.Header.Get("X-API-Key") != "secret" {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
		var body struct {
			PlayerID string `json:"player_id"`
			Score    *int64 `json:"score"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PlayerID == "" || body.Score == nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		mu.Lock()
		players[body.PlayerID] = true
		mu.Unlock()
		// Every fifth request is rejected as if the queue were full.
		if served.Add(1)%5 == 0 {
			rejected.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	cfg := config{
		URL: srv.URL, Board: "b1", Concurrency: 4, Duration: 200 * time.Millisecond,
		Players: 0, Timeout: time.Second, APIKey: "secret",
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()
	s := run(ctx, cfg, newClient(cfg))

	if s.Total == 0 {
		t.Fatal("no requests were sent")
	}
	if got := int64(s.Total); got != served.Load() {
		t.Errorf("counted %d requests, server saw %d", got, served.Load())
	}
	if got := int64(s.Statuses[503]); got != rejected.Load() {
		t.Errorf("counted %d 503s, server sent %d", got, rejected.Load())
	}
	if s.Success != s.Statuses[202] || s.Success+s.Statuses[503] != s.Total {
		t.Errorf("unexpected statuses %v (success %d, total %d)", s.Statuses, s.Success, s.Total)
	}
	// -players 0 must give every request its own player.
	if len(players) != s.Total {
		t.Errorf("%d distinct players for %d requests", len(players), s.Total)
	}
	// The deadline starts above, a moment before run starts its own clock,
	// so Elapsed can be a little under Duration. It must not end early.
	if floor := cfg.Duration - 20*time.Millisecond; s.Elapsed < floor {
		t.Errorf("elapsed %s, want at least %s: run stopped before the deadline", s.Elapsed, floor)
	}
}

// TestRunUnreachable checks that transport errors are counted, not lost.
func TestRunUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens on this port any more

	cfg := config{
		URL: url, Board: "b1", Concurrency: 2, Duration: 50 * time.Millisecond,
		Players: 10, Timeout: time.Second, APIKey: "k",
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()
	s := run(ctx, cfg, newClient(cfg))

	if s.Total == 0 || s.Success != 0 {
		t.Fatalf("total=%d success=%d, want >0 and 0", s.Total, s.Success)
	}
	// Every request must land in the "connection refused" bucket, on Windows
	// (WSAECONNREFUSED) as well as on Linux (ECONNREFUSED).
	if got := s.NetErrors["connection refused"]; got != s.Total || len(s.NetErrors) != 1 {
		t.Errorf("want all %d requests as connection refused, got %v", s.Total, s.NetErrors)
	}
}

func TestTicksRate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		tokens := ticks(ctx, 20)

		got := 0
		for {
			select {
			case <-tokens:
				got++
				continue
			case <-ctx.Done():
			}
			break
		}
		// 20 per second; the last tick may race with the deadline.
		if got < 19 || got > 20 {
			t.Errorf("got %d tokens in 1s at rate 20, want 19 or 20", got)
		}
	})
}

func TestErrKind(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{context.DeadlineExceeded, "timeout"},
		{fmt.Errorf("x: %w", errConnRefused), "connection refused"},
		{fmt.Errorf("x: %w", errConnReset), "connection reset"},
		{io.ErrUnexpectedEOF, "connection closed (EOF)"},
		{errors.New("boom"), "other: boom"},
	}
	for _, tt := range tests {
		if got := errKind(tt.err); got != tt.want {
			t.Errorf("errKind(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}
