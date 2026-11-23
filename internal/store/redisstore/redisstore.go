// Package redisstore implements leaderboard.Store on Redis.
//
// Keys:
//
//	lb:{board}     sorted set, member = player ID, score = score
//	player:{id}    hash, field "name"
package redisstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/agahatay/leaderboard/internal/leaderboard"
)

// Store is a leaderboard.Store backed by Redis.
type Store struct {
	rdb     *redis.Client
	timeout time.Duration
}

var _ leaderboard.Store = (*Store)(nil)

// New returns a Store that bounds every Redis call by timeout. The client
// should be created with ContextTimeoutEnabled so that the deadline is
// applied to network reads and writes, not only to waiting for a connection.
func New(rdb *redis.Client, timeout time.Duration) *Store {
	return &Store{rdb: rdb, timeout: timeout}
}

func boardKey(board string) string { return "lb:" + board }
func playerKey(id string) string   { return "player:" + id }

func (s *Store) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.timeout)
}

// SubmitScore uses ZADD GT: the score only ever goes up, so retrying the
// same request is harmless.
func (s *Store) SubmitScore(ctx context.Context, board, playerID string, score int64) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	key := boardKey(board)
	// Scores up to 1e12 are exact in a float64 (exact up to 2^53).
	if err := s.rdb.ZAddGT(ctx, key, redis.Z{Score: float64(score), Member: playerID}).Err(); err != nil {
		return fmt.Errorf("zadd %s: %w", key, err)
	}
	return nil
}

// SubmitScores sends one ZADD GT per event in a single pipeline: one round
// trip for the whole batch.
func (s *Store) SubmitScores(ctx context.Context, events []leaderboard.ScoreEvent) error {
	if len(events) == 0 {
		return nil
	}
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	pipe := s.rdb.Pipeline()
	for _, ev := range events {
		pipe.ZAddGT(ctx, boardKey(ev.Board), redis.Z{Score: float64(ev.Score), Member: ev.PlayerID})
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("pipeline zadd (%d events): %w", len(events), err)
	}
	return nil
}

// Top reads the best n members, then fetches all names in one pipeline.
func (s *Store) Top(ctx context.Context, board string, n int) ([]leaderboard.Entry, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	return s.rangeWithNames(ctx, boardKey(board), 0, int64(n-1))
}

// Around finds the player's 0-based rank r, then reads r-radius..r+radius.
// These are two round trips, not one atomic read: a write in between can
// shift the window by a place (documented in README).
func (s *Store) Around(ctx context.Context, board, playerID string, radius int) ([]leaderboard.Entry, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	key := boardKey(board)
	r, err := s.rdb.ZRevRank(ctx, key, playerID).Result()
	if errors.Is(err, redis.Nil) {
		return nil, leaderboard.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("zrevrank %s %s: %w", key, playerID, err)
	}
	// Redis clamps a stop index past the end; we only clamp the start.
	return s.rangeWithNames(ctx, key, max(0, r-int64(radius)), r+int64(radius))
}

// rangeWithNames reads 0-based ranks start..stop (highest score first) and
// fills in names with one pipeline of HGET.
func (s *Store) rangeWithNames(ctx context.Context, key string, start, stop int64) ([]leaderboard.Entry, error) {
	zs, err := s.rdb.ZRangeArgsWithScores(ctx, redis.ZRangeArgs{
		Key: key, Start: start, Stop: stop, Rev: true,
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("zrange %s: %w", key, err)
	}

	entries := make([]leaderboard.Entry, len(zs))
	if len(zs) == 0 {
		return entries, nil
	}

	pipe := s.rdb.Pipeline()
	names := make([]*redis.StringCmd, len(zs))
	for i, z := range zs {
		id, ok := z.Member.(string)
		if !ok {
			return nil, fmt.Errorf("zrange %s: unexpected member type %T", key, z.Member)
		}
		entries[i] = leaderboard.Entry{Rank: start + int64(i) + 1, PlayerID: id, Score: int64(z.Score)}
		names[i] = pipe.HGet(ctx, playerKey(id), "name")
	}
	// Exec reports redis.Nil when any player has no name; that is expected.
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("hget names for %s: %w", key, err)
	}
	for i, cmd := range names {
		name, err := cmd.Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("hget %s: %w", playerKey(entries[i].PlayerID), err)
		}
		entries[i].Name = name
	}
	return entries, nil
}

// PlayerRank reads rank and score in one MULTI/EXEC so they come from the
// same moment; a plain pipeline could interleave with another write.
func (s *Store) PlayerRank(ctx context.Context, board, playerID string) (leaderboard.PlayerRank, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	key := boardKey(board)
	var rankCmd *redis.IntCmd
	var scoreCmd *redis.FloatCmd
	_, err := s.rdb.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		rankCmd = pipe.ZRevRank(ctx, key, playerID)
		scoreCmd = pipe.ZScore(ctx, key, playerID)
		return nil
	})
	if errors.Is(err, redis.Nil) {
		return leaderboard.PlayerRank{}, leaderboard.ErrNotFound
	}
	if err != nil {
		return leaderboard.PlayerRank{}, fmt.Errorf("zrevrank/zscore %s %s: %w", key, playerID, err)
	}
	return leaderboard.PlayerRank{Rank: rankCmd.Val() + 1, Score: int64(scoreCmd.Val())}, nil
}

// Ping checks that Redis answers. The caller sets the deadline (/readyz uses
// 500ms), so REDIS_TIMEOUT is not applied here.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis ping: %w", err)
	}
	return nil
}

func (s *Store) SetName(ctx context.Context, playerID, name string) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	key := playerKey(playerID)
	if err := s.rdb.HSet(ctx, key, "name", name).Err(); err != nil {
		return fmt.Errorf("hset %s: %w", key, err)
	}
	return nil
}
