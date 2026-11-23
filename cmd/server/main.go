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
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/agahatay/leaderboard/internal/api"
	"github.com/agahatay/leaderboard/internal/config"
	"github.com/agahatay/leaderboard/internal/leaderboard"
	"github.com/agahatay/leaderboard/internal/store/redisstore"
)

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

	if err := run(logger, cfg); err != nil {
		logger.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger, cfg config.Config) error {
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		// Make go-redis honour our per-call context deadlines (REDIS_TIMEOUT).
		ContextTimeoutEnabled: true,
	})
	defer func() {
		if err := rdb.Close(); err != nil {
			logger.Error("close redis client", "err", err)
		}
	}()

	// Not fatal: the process stays up and requests fail with 500 until Redis
	// is reachable. /readyz (v0.2) will report this state to orchestrators.
	pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		logger.Warn("redis not reachable at startup", "addr", cfg.RedisAddr, "err", err)
	}
	cancel()

	store := redisstore.New(rdb, cfg.RedisTimeout)
	svc := leaderboard.NewService(store)

	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.Port),
		Handler:           api.NewHandler(svc, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	logger.Info("server starting", "addr", srv.Addr, "redis_addr", cfg.RedisAddr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("listen: %w", err)
	}
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
