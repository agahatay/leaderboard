package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/agahatay/leaderboard/internal/leaderboard"
)

// fakeWriter records batches. It can fail the first failFirst calls, and if
// block is non-nil every call waits until block is closed.
type fakeWriter struct {
	block     chan struct{}
	failFirst int

	mu      sync.Mutex
	calls   int
	batches [][]leaderboard.ScoreEvent
}

func (f *fakeWriter) SubmitScores(_ context.Context, events []leaderboard.ScoreEvent) error {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= f.failFirst {
		return errors.New("write failed")
	}
	// Clone: the ingester reuses the slice after we return.
	f.batches = append(f.batches, slices.Clone(events))
	return nil
}

func (f *fakeWriter) sizes() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []int{}
	for _, b := range f.batches {
		out = append(out, len(b))
	}
	return out
}

func (f *fakeWriter) playerIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, b := range f.batches {
		for _, ev := range b {
			out = append(out, ev.PlayerID)
		}
	}
	slices.Sort(out)
	return out
}

func newIngester(t *testing.T, w BatchWriter, cfg Config, logger *slog.Logger) *Ingester {
	t.Helper()
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	ing, err := New(w, cfg, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return ing
}

func event(n int) leaderboard.ScoreEvent {
	return leaderboard.ScoreEvent{Board: "b", PlayerID: fmt.Sprintf("p%04d", n), Score: int64(n)}
}

func mustEnqueue(t *testing.T, ing *Ingester, from, to int) {
	t.Helper()
	for n := from; n < to; n++ {
		if err := ing.Enqueue(event(n)); err != nil {
			t.Fatalf("Enqueue(%d): %v", n, err)
		}
	}
}

func TestFlushOnSize(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &fakeWriter{}
		ing := newIngester(t, w, Config{QueueSize: 10, Workers: 1, BatchSize: 3, FlushInterval: time.Hour}, nil)

		mustEnqueue(t, ing, 0, 3)
		synctest.Wait() // until the worker is idle again
		if got := w.sizes(); !slices.Equal(got, []int{3}) {
			t.Fatalf("after 3 events: batches %v, want [3]", got)
		}

		mustEnqueue(t, ing, 3, 5)
		synctest.Wait()
		if got := w.sizes(); !slices.Equal(got, []int{3}) {
			t.Fatalf("2 events below BatchSize were flushed early: batches %v", got)
		}

		if err := ing.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := w.sizes(); !slices.Equal(got, []int{3, 2}) {
			t.Errorf("after Close: batches %v, want [3 2]", got)
		}
	})
}

func TestFlushOnInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &fakeWriter{}
		ing := newIngester(t, w, Config{QueueSize: 10, Workers: 1, BatchSize: 100, FlushInterval: 50 * time.Millisecond}, nil)

		mustEnqueue(t, ing, 0, 2)
		time.Sleep(49 * time.Millisecond) // fake clock inside the bubble
		synctest.Wait()
		if got := w.sizes(); len(got) != 0 {
			t.Fatalf("flushed before the interval: batches %v", got)
		}

		time.Sleep(1 * time.Millisecond)
		synctest.Wait()
		if got := w.sizes(); !slices.Equal(got, []int{2}) {
			t.Fatalf("after the interval: batches %v, want [2]", got)
		}

		if err := ing.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDrainOnClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &fakeWriter{}
		// Long interval and big batches: only Close can flush these events.
		ing := newIngester(t, w, Config{QueueSize: 1000, Workers: 4, BatchSize: 1000, FlushInterval: time.Hour}, nil)

		mustEnqueue(t, ing, 0, 250)
		if err := ing.Close(t.Context()); err != nil {
			t.Fatal(err)
		}

		want := make([]string, 250)
		for n := range want {
			want[n] = event(n).PlayerID
		}
		if got := w.playerIDs(); !slices.Equal(got, want) {
			t.Errorf("written %d events, want each of the 250 exactly once", len(got))
		}
	})
}

func TestEnqueueWhenFull(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &fakeWriter{block: make(chan struct{})}
		ing := newIngester(t, w, Config{QueueSize: 2, Workers: 1, BatchSize: 1, FlushInterval: time.Hour}, nil)

		mustEnqueue(t, ing, 0, 1)
		synctest.Wait() // the worker took event 0 and is now stuck in the writer
		mustEnqueue(t, ing, 1, 3)

		if err := ing.Enqueue(event(3)); !errors.Is(err, ErrQueueFull) {
			t.Fatalf("Enqueue on full queue: err = %v, want ErrQueueFull", err)
		}

		close(w.block)
		if err := ing.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := len(w.playerIDs()); got != 3 {
			t.Errorf("written %d events, want the 3 accepted ones", got)
		}
	})
}

func TestEnqueueAfterClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ing := newIngester(t, &fakeWriter{}, Config{QueueSize: 1, Workers: 1, BatchSize: 1, FlushInterval: time.Second}, nil)
		if err := ing.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := ing.Enqueue(event(0)); !errors.Is(err, ErrClosed) {
			t.Errorf("err = %v, want ErrClosed", err)
		}
		if err := ing.Close(t.Context()); err != nil {
			t.Errorf("second Close: %v", err)
		}
	})
}

func TestRetryThenSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &fakeWriter{failFirst: 2}
		ing := newIngester(t, w, Config{
			QueueSize: 1, Workers: 1, BatchSize: 1, FlushInterval: time.Hour,
			MaxAttempts: 3, RetryBackoff: 10 * time.Millisecond,
		}, nil)

		start := time.Now()
		mustEnqueue(t, ing, 0, 1)
		if err := ing.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		if w.calls != 3 || len(w.playerIDs()) != 1 {
			t.Errorf("calls %d, written %d; want 3 calls and the event written", w.calls, len(w.playerIDs()))
		}
		// Backoff 10ms then 20ms (fake time, so this is exact).
		if d := time.Since(start); d != 30*time.Millisecond {
			t.Errorf("retries took %v, want 30ms", d)
		}
	})
}

func TestDropAfterMaxAttempts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logs, nil))
		w := &fakeWriter{failFirst: 1_000_000} // always fails
		ing := newIngester(t, w, Config{
			QueueSize: 2, Workers: 1, BatchSize: 1, FlushInterval: time.Hour, MaxAttempts: 3,
		}, logger)

		mustEnqueue(t, ing, 0, 2)
		if err := ing.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		// Two batches, three attempts each; the worker kept going after the first drop.
		if w.calls != 6 {
			t.Errorf("calls = %d, want 6", w.calls)
		}
		if n := strings.Count(logs.String(), "dropping score batch"); n != 2 {
			t.Errorf("logged %d drops, want 2; logs:\n%s", n, logs.String())
		}
	})
}

func TestCloseTimesOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &fakeWriter{block: make(chan struct{})}
		ing := newIngester(t, w, Config{QueueSize: 1, Workers: 1, BatchSize: 1, FlushInterval: time.Hour}, nil)
		mustEnqueue(t, ing, 0, 1)
		synctest.Wait()

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := ing.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want DeadlineExceeded", err)
		}

		// Let the worker finish so no goroutine outlives the test.
		close(w.block)
		if err := ing.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

// Many producers race with Close. Every event that Enqueue accepted must be
// written exactly once; rejected ones must not be. Run with -race.
func TestNoAcceptedEventLostOnConcurrentClose(t *testing.T) {
	w := &fakeWriter{}
	ing := newIngester(t, w, Config{QueueSize: 64, Workers: 4, BatchSize: 16, FlushInterval: 5 * time.Millisecond}, nil)

	const producers = 8
	accepted := make([][]string, producers)
	var wg sync.WaitGroup
	for g := range producers {
		wg.Go(func() {
			for n := 0; ; n++ {
				ev := leaderboard.ScoreEvent{Board: "b", PlayerID: fmt.Sprintf("g%d-%06d", g, n), Score: 1}
				switch err := ing.Enqueue(ev); {
				case err == nil:
					accepted[g] = append(accepted[g], ev.PlayerID)
				case errors.Is(err, ErrQueueFull):
					runtime.Gosched()
				case errors.Is(err, ErrClosed):
					return
				default:
					t.Errorf("Enqueue: %v", err)
					return
				}
			}
		})
	}

	time.Sleep(50 * time.Millisecond) // let the producers run
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := ing.Close(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()

	want := slices.Concat(accepted...)
	slices.Sort(want)
	got := w.playerIDs()
	if len(want) == 0 {
		t.Fatal("no events accepted; the test did not exercise anything")
	}
	if !slices.Equal(got, want) {
		t.Fatalf("accepted %d events, written %d; they must match exactly", len(want), len(got))
	}
	t.Logf("accepted and written: %d events", len(want))
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	tests := []Config{
		{QueueSize: 0, Workers: 1, BatchSize: 1, FlushInterval: time.Second},
		{QueueSize: 1, Workers: 0, BatchSize: 1, FlushInterval: time.Second},
		{QueueSize: 1, Workers: 1, BatchSize: 0, FlushInterval: time.Second},
		{QueueSize: 1, Workers: 1, BatchSize: 1, FlushInterval: 0},
	}
	for _, cfg := range tests {
		if _, err := New(&fakeWriter{}, cfg, slog.New(slog.DiscardHandler)); err == nil {
			t.Errorf("New(%+v) succeeded, want error", cfg)
		}
	}
}
