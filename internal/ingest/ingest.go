// Package ingest is the async write path: a bounded queue in front of a pool
// of workers that write scores in batches.
//
// Lifecycle: New starts the workers. Enqueue never blocks. Close stops
// accepting, lets the workers drain and flush everything already queued, and
// waits for them. Only this package closes the channel.
//
// Trade-off: a score accepted by Enqueue lives only in memory until its batch
// is written. If the process crashes (not a graceful stop), it is lost.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/agahatay/leaderboard/internal/leaderboard"
)

var (
	// ErrQueueFull means the queue has no free slot; the client should retry.
	ErrQueueFull = errors.New("ingest queue is full")
	// ErrClosed means Close has been called; no more events are accepted.
	ErrClosed = errors.New("ingest is shut down")
)

// BatchWriter writes a batch of score events. It must bound its own call
// time (RedisStore applies REDIS_TIMEOUT) and must not keep the slice.
type BatchWriter interface {
	SubmitScores(ctx context.Context, events []leaderboard.ScoreEvent) error
}

// Config sizes the queue and the worker pool.
type Config struct {
	QueueSize     int
	Workers       int
	BatchSize     int
	FlushInterval time.Duration
	// MaxAttempts is how often a failing batch is tried before it is
	// dropped and logged. Default 3.
	MaxAttempts int
	// RetryBackoff is the wait after the first failure; it doubles after
	// each further failure. Default 50ms.
	RetryBackoff time.Duration
}

// Ingester owns the queue and its workers.
type Ingester struct {
	w      BatchWriter
	cfg    Config
	logger *slog.Logger
	ch     chan leaderboard.ScoreEvent

	// mu guards closed and the close of ch. Enqueue holds the read lock
	// while sending, so close(ch) can never race with a send (which would
	// panic). Senders never block, so the write lock is quick to get.
	mu     sync.RWMutex
	closed bool

	done chan struct{} // closed when every worker has returned
}

// New validates cfg and starts cfg.Workers goroutines.
func New(w BatchWriter, cfg Config, logger *slog.Logger) (*Ingester, error) {
	if cfg.QueueSize < 1 || cfg.Workers < 1 || cfg.BatchSize < 1 || cfg.FlushInterval <= 0 {
		return nil, fmt.Errorf("ingest: invalid config %+v", cfg)
	}
	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 3
	}
	if cfg.RetryBackoff <= 0 {
		cfg.RetryBackoff = 50 * time.Millisecond
	}

	i := &Ingester{
		w:      w,
		cfg:    cfg,
		logger: logger,
		ch:     make(chan leaderboard.ScoreEvent, cfg.QueueSize),
		done:   make(chan struct{}),
	}
	var wg sync.WaitGroup
	for range cfg.Workers {
		wg.Go(i.worker)
	}
	go func() {
		wg.Wait()
		close(i.done)
	}()
	return i, nil
}

// Enqueue queues ev without blocking. It returns ErrQueueFull when there is
// no free slot and ErrClosed after Close.
func (i *Ingester) Enqueue(ev leaderboard.ScoreEvent) error {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.closed {
		return ErrClosed
	}
	select {
	case i.ch <- ev:
		return nil
	default:
		return ErrQueueFull
	}
}

// Close stops accepting events and waits until the workers have flushed
// everything that was queued. If ctx ends first it returns an error; the
// workers keep flushing in the background until the process exits.
// Calling Close more than once is safe.
func (i *Ingester) Close(ctx context.Context) error {
	i.mu.Lock()
	if !i.closed {
		i.closed = true
		close(i.ch)
	}
	i.mu.Unlock()

	select {
	case <-i.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("ingest close: workers still flushing: %w", ctx.Err())
	}
}

// worker collects events into a batch and flushes it when it reaches
// BatchSize, when FlushInterval ticks, or when the channel is closed.
func (i *Ingester) worker() {
	batch := make([]leaderboard.ScoreEvent, 0, i.cfg.BatchSize)
	ticker := time.NewTicker(i.cfg.FlushInterval)
	defer ticker.Stop()

	for {
		select {
		case ev, ok := <-i.ch:
			if !ok { // closed and fully drained
				i.flush(batch)
				return
			}
			batch = append(batch, ev)
			if len(batch) >= i.cfg.BatchSize {
				i.flush(batch)
				batch = batch[:0] // reuse the backing array
			}
		case <-ticker.C:
			if len(batch) > 0 {
				i.flush(batch)
				batch = batch[:0]
			}
		}
	}
}

// flush writes batch, retrying with exponential backoff. ZADD GT makes a
// retry safe even if an earlier attempt partly succeeded. After MaxAttempts
// failures the batch is dropped and logged.
func (i *Ingester) flush(batch []leaderboard.ScoreEvent) {
	if len(batch) == 0 {
		return
	}
	backoff := i.cfg.RetryBackoff
	var err error
	for attempt := 1; attempt <= i.cfg.MaxAttempts; attempt++ {
		// Not tied to any request: the request that queued the event is
		// long finished. The writer applies its own timeout.
		if err = i.w.SubmitScores(context.Background(), batch); err == nil {
			return
		}
		if attempt < i.cfg.MaxAttempts {
			i.logger.Warn("score batch write failed, retrying",
				"attempt", attempt, "events", len(batch), "retry_in", backoff, "err", err)
			time.Sleep(backoff)
			backoff *= 2
		}
	}
	i.logger.Error("dropping score batch after retries",
		"attempts", i.cfg.MaxAttempts, "events", len(batch), "err", err)
}
