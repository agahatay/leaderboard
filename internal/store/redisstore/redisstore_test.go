package redisstore_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/agahatay/leaderboard/internal/leaderboard"
	"github.com/agahatay/leaderboard/internal/leaderboard/storetest"
	"github.com/agahatay/leaderboard/internal/store/redisstore"
)

// newClient connects to REDIS_ADDR or skips the test when it is unset.
func newClient(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("REDIS_ADDR not set; skipping Redis integration test")
	}
	rdb := redis.NewClient(&redis.Options{
		Addr:                  addr,
		Password:              os.Getenv("REDIS_PASSWORD"),
		ContextTimeoutEnabled: true,
	})
	t.Cleanup(func() { _ = rdb.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("REDIS_ADDR=%s is set but Redis is not reachable: %v", addr, err)
	}
	return rdb
}

// deleteKeys removes every key matching pattern. It runs in t.Cleanup, after
// t.Context() is already cancelled, so it uses its own context.
func deleteKeys(t *testing.T, rdb *redis.Client, pattern string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	iter := rdb.Scan(ctx, 0, pattern, 100).Iterator()
	for iter.Next(ctx) {
		if err := rdb.Del(ctx, iter.Val()).Err(); err != nil {
			t.Errorf("cleanup del %s: %v", iter.Val(), err)
		}
	}
	if err := iter.Err(); err != nil {
		t.Errorf("cleanup scan %s: %v", pattern, err)
	}
}

func TestRedisStore(t *testing.T) {
	rdb := newClient(t)
	storetest.Run(t, func(t *testing.T) leaderboard.Store {
		p := storetest.Prefix(t)
		t.Cleanup(func() {
			deleteKeys(t, rdb, "lb:"+p+"*")
			deleteKeys(t, rdb, "player:"+p+"*")
		})
		return redisstore.New(rdb, 2*time.Second)
	})
}

// Uses the key layout documented in CLAUDE.md, so other tools (redis-cli,
// a future migration) can rely on it.
func TestRedisStoreKeyLayout(t *testing.T) {
	rdb := newClient(t)
	p := storetest.Prefix(t)
	t.Cleanup(func() {
		deleteKeys(t, rdb, "lb:"+p+"*")
		deleteKeys(t, rdb, "player:"+p+"*")
	})
	s := redisstore.New(rdb, 2*time.Second)
	ctx := t.Context()

	if err := s.SubmitScore(ctx, p, p+"_a", 42); err != nil {
		t.Fatal(err)
	}
	if err := s.SetName(ctx, p+"_a", "Ayşe"); err != nil {
		t.Fatal(err)
	}
	if got, err := rdb.ZScore(ctx, "lb:"+p, p+"_a").Result(); err != nil || got != 42 {
		t.Errorf("ZSCORE lb:%s = %v, %v; want 42", p, got, err)
	}
	if got, err := rdb.HGet(ctx, "player:"+p+"_a", "name").Result(); err != nil || got != "Ayşe" {
		t.Errorf("HGET player:%s_a name = %q, %v; want Ayşe", p, got, err)
	}
}

// A down Redis must surface as a wrapped error, never as ErrNotFound (which
// the API would turn into a misleading 404). Needs no running Redis.
func TestRedisStoreUnreachable(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{
		Addr:                  "127.0.0.1:1", // nothing listens on port 1
		ContextTimeoutEnabled: true,
		MaxRetries:            -1, // fail on the first attempt
	})
	t.Cleanup(func() { _ = rdb.Close() })
	s := redisstore.New(rdb, 500*time.Millisecond)

	tests := []struct {
		name    string
		call    func(context.Context) error
		wantCmd string
	}{
		{"submit", func(ctx context.Context) error { return s.SubmitScore(ctx, "b", "p", 1) }, "zadd lb:b"},
		{"top", func(ctx context.Context) error { _, err := s.Top(ctx, "b", 10); return err }, "zrange lb:b"},
		{"rank", func(ctx context.Context) error { _, err := s.PlayerRank(ctx, "b", "p"); return err }, "lb:b"},
		{"name", func(ctx context.Context) error { return s.SetName(ctx, "p", "x") }, "hset player:p"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			err := tt.call(t.Context())
			if err == nil {
				t.Fatal("expected an error")
			}
			if errors.Is(err, leaderboard.ErrNotFound) {
				t.Errorf("err = %v, must not be ErrNotFound", err)
			}
			if !strings.Contains(err.Error(), tt.wantCmd) {
				t.Errorf("err = %q, want it to mention %q", err, tt.wantCmd)
			}
			if d := time.Since(start); d > 2*time.Second {
				t.Errorf("took %v, want it bounded by the store timeout", d)
			}
		})
	}
}
