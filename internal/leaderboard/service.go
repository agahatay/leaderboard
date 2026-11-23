package leaderboard

import (
	"context"
	"fmt"
)

// Service validates input and delegates to a Store.
type Service struct {
	store Store
}

// NewService returns a Service backed by store.
func NewService(store Store) *Service {
	return &Service{store: store}
}

// SubmitScore validates and records a score.
func (s *Service) SubmitScore(ctx context.Context, board, playerID string, score int64) error {
	if err := validateID("board", board); err != nil {
		return err
	}
	if err := validateID("player_id", playerID); err != nil {
		return err
	}
	if err := validateScore(score); err != nil {
		return err
	}
	if err := s.store.SubmitScore(ctx, board, playerID, score); err != nil {
		return fmt.Errorf("submit score: %w", err)
	}
	return nil
}

// Top returns the best limit entries of board.
func (s *Service) Top(ctx context.Context, board string, limit int) ([]Entry, error) {
	if err := validateID("board", board); err != nil {
		return nil, err
	}
	if err := validateLimit(limit); err != nil {
		return nil, err
	}
	entries, err := s.store.Top(ctx, board, limit)
	if err != nil {
		return nil, fmt.Errorf("top: %w", err)
	}
	return entries, nil
}

// PlayerRank returns the player's rank on board, or an error wrapping ErrNotFound.
func (s *Service) PlayerRank(ctx context.Context, board, playerID string) (PlayerRank, error) {
	if err := validateID("board", board); err != nil {
		return PlayerRank{}, err
	}
	if err := validateID("player_id", playerID); err != nil {
		return PlayerRank{}, err
	}
	pr, err := s.store.PlayerRank(ctx, board, playerID)
	if err != nil {
		return PlayerRank{}, fmt.Errorf("player rank: %w", err)
	}
	return pr, nil
}

// SetName validates and stores a player's display name.
func (s *Service) SetName(ctx context.Context, playerID, name string) error {
	if err := validateID("player_id", playerID); err != nil {
		return err
	}
	if err := validateName(name); err != nil {
		return err
	}
	if err := s.store.SetName(ctx, playerID, name); err != nil {
		return fmt.Errorf("set name: %w", err)
	}
	return nil
}
