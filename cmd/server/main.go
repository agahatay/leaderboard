// Command server runs the leaderboard HTTP service.
//
// With -healthcheck it instead sends GET /healthz to the local server and
// exits 0 on HTTP 200, 1 otherwise. The runtime image has no curl, so Docker
// health checks call the binary itself.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/agahatay/leaderboard/internal/api"
	"github.com/agahatay/leaderboard/internal/config"
	"github.com/agahatay/leaderboard/internal/ingest"
	"github.com/agahatay/leaderboard/internal/leaderboard"
	"github.com/agahatay/leaderboard/internal/store/redisstore"
)

// commit is set at build time by the Dockerfile:
// go build -ldflags "-X main.commit=<sha>". Empty in other builds.
var commit string

// buildCommit returns the git commit this binary runs, from the first
// source that has it:
//
//  1. commit, set by -ldflags (Docker images built by CI);
//  2. RENDER_GIT_COMMIT, which Render sets at runtime;
//  3. the VCS stamp go build adds in a git checkout (local builds);
//  4. "unknown".
func buildCommit(getenv func(string) string, info *debug.BuildInfo) string {
	if commit != "" {
		return commit
	}
	if c := getenv("RENDER_GIT_COMMIT"); c != "" {
		return c
	}
	if info != nil {
		var rev string
		var dirty bool
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
		if rev != "" {
			if dirty {
				rev += "-dirty" // built with uncommitted changes
			}
			return rev
		}
	}
	return "unknown"
}

func main() {
	healthcheck := flag.Bool("healthcheck", false, "GET http://localhost:$PORT/healthz and exit 0 if it returns 200")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		logger.Error("invalid configuration", "err", err)
		os.Exit(1)
	}

	if *healthcheck {
		url := "http://localhost:" + strconv.Itoa(cfg.Port) + "/healthz"
		if err := checkHealth(context.Background(), url); err != nil {
			fmt.Fprintln(os.Stderr, "healthcheck failed:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	// ctx is cancelled on Ctrl+C (SIGINT) or SIGTERM (docker stop).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	info, _ := debug.ReadBuildInfo()
	if err := run(ctx, logger, cfg, buildCommit(os.Getenv, info)); err != nil {
		logger.Error("server stopped with error", "err", err)
		os.Exit(1)
	}
}

// run connects to Redis and serves until ctx is cancelled.
func run(ctx context.Context, logger *slog.Logger, cfg config.Config, commit string) error {
	opts, err := cfg.RedisOptions() // REDIS_URL if set, else REDIS_ADDR
	if err != nil {
		return err
	}
	// Make go-redis honour our per-call context deadlines (REDIS_TIMEOUT).
	opts.ContextTimeoutEnabled = true
	rdb := redis.NewClient(opts)
	defer func() {
		if err := rdb.Close(); err != nil {
			logger.Error("close redis client", "err", err)
		}
	}()
	store := redisstore.New(rdb, cfg.RedisTimeout)

	// Not fatal: the process stays up, /readyz answers 503 and requests
	// fail with 500 until Redis is reachable.
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	if err := store.Ping(pingCtx); err != nil {
		// Only host:port is logged; the password stays in opts.
		logger.Warn("redis not reachable at startup", "addr", opts.Addr, "err", err)
	}
	cancel()

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", ":"+strconv.Itoa(cfg.Port))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	return serve(ctx, logger, cfg, commit, store, store, ln)
}

// serve runs the HTTP server and, in async mode, the ingest workers on ln
// until ctx is cancelled, then shuts down in this order:
//
//  1. http.Server.Shutdown: stop accepting, wait for in-flight requests,
//     so no handler can enqueue any more.
//  2. ingester.Close: close the queue, workers drain and flush, wait for them.
//
// Both steps share one SHUTDOWN_TIMEOUT budget. It is separate from run so a
// test can drive the real sequence with a MemoryStore and a random port.
func serve(ctx context.Context, logger *slog.Logger, cfg config.Config, commit string,
	store leaderboard.Store, ready api.Pinger, ln net.Listener,
) error {
	deps := api.Deps{
		Service: leaderboard.NewService(store),
		Ready:   ready,
		APIKey:  cfg.APIKey,
		Logger:  logger,
		Commit:  commit,
	}

	var ing *ingest.Ingester
	if cfg.IngestMode == config.ModeAsync {
		var err error
		ing, err = ingest.New(store, ingest.Config{
			QueueSize:     cfg.QueueSize,
			Workers:       cfg.Workers,
			BatchSize:     cfg.BatchSize,
			FlushInterval: cfg.FlushInterval,
		}, logger)
		if err != nil {
			return err
		}
		// Assigned only when non-nil: a nil *Ingester stored in the
		// interface would be a non-nil interface, and the handler would
		// think it is in async mode.
		deps.Queue = ing
	}

	srv := &http.Server{
		Handler:           api.NewHandler(deps),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	logger.Info("server started", "addr", ln.Addr().String(), "ingest_mode", cfg.IngestMode, "commit", commit)

	var errs []error
	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-serveErr:
		// The server died on its own. Still drain the queue below.
		errs = append(errs, fmt.Errorf("serve: %w", err))
		serveErr = nil
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		errs = append(errs, fmt.Errorf("http shutdown: %w", err))
	}
	if serveErr != nil {
		// Serve returns ErrServerClosed right after Shutdown starts; reading
		// it makes sure that goroutine has exited.
		if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
			errs = append(errs, fmt.Errorf("serve: %w", err))
		}
	}
	if ing != nil {
		if err := ing.Close(shutdownCtx); err != nil {
			errs = append(errs, err)
		}
	}

	if err := errors.Join(errs...); err != nil {
		return err
	}
	logger.Info("shutdown complete")
	return nil
}

// checkHealth returns nil if GET url answers 200 within 2 seconds.
func checkHealth(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return nil
}
