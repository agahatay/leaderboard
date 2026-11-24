// Command loadgen sends POST /v1/leaderboards/{board}/scores requests to the
// leaderboard service and prints latency percentiles, throughput and errors.
//
// N worker goroutines send requests in a closed loop (each waits for its
// response before the next request), optionally capped by a shared rate.
// Every result goes over one channel to a single collector goroutine, so the
// counters need no locks.
//
// The API key is read from the API_KEY environment variable so that it does
// not show up in the shell history or the process list.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// config holds the parsed command line.
type config struct {
	URL         string
	Board       string
	Concurrency int
	Duration    time.Duration
	Rate        int // total requests per second over all workers; 0 = no limit
	Players     int // size of the player ID pool; 0 = a new player per request
	Timeout     time.Duration
	APIKey      string
}

func parseConfig(args []string, getenv func(string) string) (config, error) {
	var c config
	fs := flag.NewFlagSet("loadgen", flag.ContinueOnError)
	fs.StringVar(&c.URL, "url", "http://localhost:8080", "base URL of the service")
	fs.StringVar(&c.Board, "board", "loadgen", "leaderboard to write to")
	fs.IntVar(&c.Concurrency, "c", 50, "number of concurrent clients")
	fs.DurationVar(&c.Duration, "d", 30*time.Second, "test duration")
	fs.IntVar(&c.Rate, "rate", 0, "total requests per second, 0 for no limit")
	fs.IntVar(&c.Players, "players", 10000, "number of distinct player IDs, 0 for a new player per request")
	fs.DurationVar(&c.Timeout, "timeout", 5*time.Second, "per-request timeout")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	c.URL = strings.TrimRight(c.URL, "/")
	c.APIKey = getenv("API_KEY")

	switch {
	case c.APIKey == "":
		return config{}, errors.New("API_KEY environment variable is not set")
	case c.Concurrency < 1:
		return config{}, fmt.Errorf("-c must be at least 1, got %d", c.Concurrency)
	case c.Duration <= 0:
		return config{}, fmt.Errorf("-d must be positive, got %s", c.Duration)
	case c.Rate < 0:
		return config{}, fmt.Errorf("-rate must not be negative, got %d", c.Rate)
	case c.Players < 0:
		return config{}, fmt.Errorf("-players must not be negative, got %d", c.Players)
	case c.Timeout <= 0:
		return config{}, fmt.Errorf("-timeout must be positive, got %s", c.Timeout)
	}
	return c, nil
}

func main() {
	cfg, err := parseConfig(os.Args[1:], os.Getenv)
	if errors.Is(err, flag.ErrHelp) {
		os.Exit(0)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "loadgen:", err)
		os.Exit(2)
	}

	// Ctrl+C stops the test early; the summary is still printed.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, cfg.Duration)
	defer cancel()

	fmt.Printf("loadgen: POST %s/v1/leaderboards/%s/scores  c=%d d=%s rate=%d players=%d\n",
		cfg.URL, cfg.Board, cfg.Concurrency, cfg.Duration, cfg.Rate, cfg.Players)
	s := run(ctx, cfg, newClient(cfg))
	s.print(os.Stdout)
	if s.Total == 0 {
		os.Exit(1)
	}
}

// newClient keeps one idle connection per worker. With the default of two
// per host, most workers would open a new TCP connection for every request
// and the test would measure connection setup instead of the service.
func newClient(cfg config) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConns = cfg.Concurrency
	tr.MaxIdleConnsPerHost = cfg.Concurrency
	return &http.Client{Transport: tr, Timeout: cfg.Timeout}
}

// result is the outcome of one request. Status is 0 when no response arrived.
type result struct {
	Latency time.Duration
	Status  int
	Err     error
}

// run sends requests until ctx is done and returns the summary. A request
// already in flight when ctx ends is allowed to finish (bounded by the client
// timeout), so the end of the test does not show up as errors.
func run(ctx context.Context, cfg config, client *http.Client) summary {
	var tokens <-chan struct{}
	if cfg.Rate > 0 {
		tokens = ticks(ctx, cfg.Rate)
	}

	results := make(chan result, cfg.Concurrency*4)
	done := make(chan summary)
	go func() { done <- collect(results) }()

	start := time.Now()
	var wg sync.WaitGroup
	for w := range cfg.Concurrency {
		wg.Go(func() { worker(ctx, cfg, client, w, tokens, results) })
	}
	wg.Wait()
	elapsed := time.Since(start)
	close(results) // only after every sender has returned

	s := <-done
	s.Elapsed = elapsed
	return s
}

// ticks sends one token per 1/rate seconds until ctx is done. Workers that are
// all busy simply miss tokens, so rate is an upper bound.
func ticks(ctx context.Context, rate int) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second / time.Duration(rate))
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				select {
				case ch <- struct{}{}:
				default: // nobody is waiting
				}
			}
		}
	}()
	return ch
}

func worker(ctx context.Context, cfg config, client *http.Client, id int,
	tokens <-chan struct{}, results chan<- result,
) {
	url := cfg.URL + "/v1/leaderboards/" + cfg.Board + "/scores"
	// Requests are not cancelled when the test ends; see run.
	reqCtx := context.WithoutCancel(ctx)

	for n := 0; ; n++ {
		if tokens != nil {
			select {
			case <-ctx.Done():
				return
			case <-tokens:
			}
		} else if ctx.Err() != nil {
			return
		}

		var player string
		if cfg.Players == 0 {
			player = "w" + strconv.Itoa(id) + "-" + strconv.Itoa(n)
		} else {
			player = "p" + strconv.Itoa(rand.IntN(cfg.Players))
		}
		results <- send(reqCtx, client, url, cfg.APIKey, player, rand.Int64N(1_000_000))
	}
}

func send(ctx context.Context, client *http.Client, url, apiKey, player string, score int64) result {
	body, err := json.Marshal(map[string]any{"player_id": player, "score": score})
	if err != nil {
		return result{Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return result{Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return result{Latency: time.Since(start), Err: err}
	}
	// Read the body to the end so the connection can be reused.
	_, err = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return result{Latency: time.Since(start), Status: resp.StatusCode, Err: err}
}

// summary is what the collector found.
type summary struct {
	Total     int
	Success   int // 2xx
	Statuses  map[int]int
	NetErrors map[string]int // requests with no complete response, by kind
	Latencies []time.Duration
	Elapsed   time.Duration
}

// collect reads results until the channel is closed. It is the only
// goroutine that touches the summary.
func collect(results <-chan result) summary {
	s := summary{Statuses: map[int]int{}, NetErrors: map[string]int{}}
	for r := range results {
		s.Total++
		if r.Err != nil {
			s.NetErrors[errKind(r.Err)]++
			continue
		}
		s.Statuses[r.Status]++
		if r.Status >= 200 && r.Status < 300 {
			s.Success++
			s.Latencies = append(s.Latencies, r.Latency)
		}
	}
	slices.Sort(s.Latencies)
	return s
}

// errKind groups transport errors so the report stays short.
func errKind(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case errors.Is(err, errConnRefused):
		return "connection refused"
	case errors.Is(err, errConnReset):
		return "connection reset"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "connection closed (EOF)"
	default:
		return "other: " + err.Error()
	}
}

// percentile returns the nearest-rank p-th percentile (0 < p <= 100) of
// sorted, or 0 if it is empty.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(float64(len(sorted))*p/100 + 0.999999999) // ceil without float noise
	rank = min(max(rank, 1), len(sorted))
	return sorted[rank-1]
}

func (s summary) print(w io.Writer) {
	secs := s.Elapsed.Seconds()
	errs := s.Total - s.Success
	fmt.Fprintf(w, "elapsed      %.2fs\n", secs)
	fmt.Fprintf(w, "requests     %d total, %d ok (2xx), %d errors\n", s.Total, s.Success, errs)
	if s.Total > 0 && secs > 0 {
		fmt.Fprintf(w, "throughput   %.1f req/s total, %.1f req/s ok\n",
			float64(s.Total)/secs, float64(s.Success)/secs)
		fmt.Fprintf(w, "error rate   %.3f%%\n", 100*float64(errs)/float64(s.Total))
	}
	fmt.Fprintln(w, "status codes")
	for _, code := range slices.Sorted(maps.Keys(s.Statuses)) {
		fmt.Fprintf(w, "  %d  %d\n", code, s.Statuses[code])
	}
	if len(s.NetErrors) > 0 {
		fmt.Fprintln(w, "transport errors")
		for _, kind := range slices.Sorted(maps.Keys(s.NetErrors)) {
			fmt.Fprintf(w, "  %s  %d\n", kind, s.NetErrors[kind])
		}
	}
	fmt.Fprintln(w, "latency of 2xx responses")
	for _, p := range []float64{50, 95, 99, 100} {
		label := "p" + strconv.FormatFloat(p, 'f', -1, 64)
		if p == 100 {
			label = "max"
		}
		fmt.Fprintf(w, "  %-4s %s\n", label, percentile(s.Latencies, p).Round(10*time.Microsecond))
	}
}
