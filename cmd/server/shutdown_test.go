package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agahatay/leaderboard/internal/config"
	"github.com/agahatay/leaderboard/internal/leaderboard"
)

type okPinger struct{}

func (okPinger) Ping(context.Context) error { return nil }

// startServe runs serve on a random local port and returns its base URL,
// a cancel func that acts like Ctrl+C, and a channel with serve's result.
func startServe(t *testing.T, cfg config.Config, store leaderboard.Store) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, slog.New(slog.DiscardHandler), cfg, store, okPinger{}, ln) }()
	return "http://" + ln.Addr().String(), cancel, done
}

func waitServe(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned error: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not return after shutdown")
	}
}

func baseConfig() config.Config {
	return config.Config{
		APIKey: "k", IngestMode: config.ModeAsync,
		QueueSize: 64, Workers: 4, BatchSize: 50,
		// Never ticks during the test: events sitting in a worker's batch
		// can only be written by the drain on shutdown.
		FlushInterval:   time.Hour,
		ShutdownTimeout: 10 * time.Second,
	}
}

// TestShutdownLosesNoAcceptedScores is the v0.2 acceptance test: under load,
// a shutdown signal must not lose any score that got a 202.
func TestShutdownLosesNoAcceptedScores(t *testing.T) {
	store := leaderboard.NewMemoryStore()
	url, shutdown, done := startServe(t, baseConfig(), store)

	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{MaxIdleConnsPerHost: 32},
	}
	defer client.CloseIdleConnections()

	const clients = 16
	accepted := make([]map[string]int64, clients) // per client: player -> score
	var nAccepted, nRejected atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup

	for c := range clients {
		accepted[c] = map[string]int64{}
		wg.Go(func() {
			for n := 0; ; n++ {
				select {
				case <-stop:
					return
				default:
				}
				player := fmt.Sprintf("c%02d-%06d", c, n)
				score := int64(n + 1)
				body := fmt.Sprintf(`{"player_id":%q,"score":%d}`, player, score)
				req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
					url+"/v1/leaderboards/global/scores", strings.NewReader(body))
				if err != nil {
					t.Error(err)
					return
				}
				req.Header.Set("X-API-Key", "k")
				resp, err := client.Do(req)
				if err != nil {
					return // server is gone: this request was not accepted
				}
				resp.Body.Close()
				switch resp.StatusCode {
				case http.StatusAccepted:
					accepted[c][player] = score
					nAccepted.Add(1)
				case http.StatusServiceUnavailable: // queue full or shutting down
					nRejected.Add(1)
				default:
					t.Errorf("unexpected status %d", resp.StatusCode)
					return
				}
			}
		})
	}

	// Let the load build up, then "press Ctrl+C" while requests are in flight.
	deadline := time.Now().Add(5 * time.Second)
	for nAccepted.Load() < 3000 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	shutdown()
	waitServe(t, done) // HTTP stopped, queue drained, workers finished
	close(stop)
	wg.Wait()

	if nAccepted.Load() == 0 {
		t.Fatal("no scores accepted; the test exercised nothing")
	}
	var missing int
	for _, m := range accepted {
		for player, score := range m {
			pr, err := store.PlayerRank(context.Background(), "global", player)
			if err != nil || pr.Score != score {
				missing++
				if missing <= 5 {
					t.Errorf("accepted %s=%d but store has %+v, %v", player, score, pr, err)
				}
			}
		}
	}
	if missing > 0 {
		t.Fatalf("%d of %d accepted scores lost on shutdown", missing, nAccepted.Load())
	}
	t.Logf("accepted %d, rejected with 503 %d, lost 0", nAccepted.Load(), nRejected.Load())
}

// In sync mode there is no queue: writes return 200 and are stored at once.
func TestServeSyncMode(t *testing.T) {
	cfg := baseConfig()
	cfg.IngestMode = config.ModeSync
	store := leaderboard.NewMemoryStore()
	url, shutdown, done := startServe(t, cfg, store)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		url+"/v1/leaderboards/global/scores", strings.NewReader(`{"player_id":"p","score":5}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-API-Key", "k")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if pr, err := store.PlayerRank(t.Context(), "global", "p"); err != nil || pr.Score != 5 {
		t.Errorf("store has %+v, %v; want score 5 right away", pr, err)
	}

	shutdown()
	waitServe(t, done)
}
