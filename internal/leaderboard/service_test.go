package leaderboard

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestServiceValidation(t *testing.T) {
	svc := NewService(NewMemoryStore())

	tests := []struct {
		name      string
		call      func(context.Context, *Service) error
		wantField string // empty means the call must succeed
	}{
		{"valid score", func(ctx context.Context, s *Service) error { return s.SubmitScore(ctx, "b", "p", 10) }, ""},
		{"max score", func(ctx context.Context, s *Service) error { return s.SubmitScore(ctx, "b", "p", MaxScore) }, ""},
		{"id with dash and underscore", func(ctx context.Context, s *Service) error { return s.SubmitScore(ctx, "my-board_1", "P_1-x", 1) }, ""},
		{"64 char id", func(ctx context.Context, s *Service) error {
			return s.SubmitScore(ctx, strings.Repeat("a", 64), "p", 1)
		}, ""},
		{"empty board", func(ctx context.Context, s *Service) error { return s.SubmitScore(ctx, "", "p", 1) }, "board"},
		{"65 char board", func(ctx context.Context, s *Service) error {
			return s.SubmitScore(ctx, strings.Repeat("a", 65), "p", 1)
		}, "board"},
		{"board with space", func(ctx context.Context, s *Service) error { return s.SubmitScore(ctx, "a b", "p", 1) }, "board"},
		{"board with non-ascii", func(ctx context.Context, s *Service) error { return s.SubmitScore(ctx, "çay", "p", 1) }, "board"},
		{"empty player", func(ctx context.Context, s *Service) error { return s.SubmitScore(ctx, "b", "", 1) }, "player_id"},
		{"player with dot", func(ctx context.Context, s *Service) error { return s.SubmitScore(ctx, "b", "a.b", 1) }, "player_id"},
		{"negative score", func(ctx context.Context, s *Service) error { return s.SubmitScore(ctx, "b", "p", -1) }, "score"},
		{"score above max", func(ctx context.Context, s *Service) error { return s.SubmitScore(ctx, "b", "p", MaxScore+1) }, "score"},

		{"limit 1", func(ctx context.Context, s *Service) error { _, err := s.Top(ctx, "b", 1); return err }, ""},
		{"limit max", func(ctx context.Context, s *Service) error { _, err := s.Top(ctx, "b", MaxLimit); return err }, ""},
		{"limit 0", func(ctx context.Context, s *Service) error { _, err := s.Top(ctx, "b", 0); return err }, "limit"},
		{"limit above max", func(ctx context.Context, s *Service) error { _, err := s.Top(ctx, "b", MaxLimit+1); return err }, "limit"},
		{"top bad board", func(ctx context.Context, s *Service) error { _, err := s.Top(ctx, "b!", 10); return err }, "board"},

		{"rank bad player", func(ctx context.Context, s *Service) error { _, err := s.PlayerRank(ctx, "b", "p p"); return err }, "player_id"},

		{"valid name", func(ctx context.Context, s *Service) error { return s.SetName(ctx, "p", "Ayşe") }, ""},
		{"64 rune name", func(ctx context.Context, s *Service) error { return s.SetName(ctx, "p", strings.Repeat("ş", 64)) }, ""},
		{"empty name", func(ctx context.Context, s *Service) error { return s.SetName(ctx, "p", "") }, "name"},
		{"blank name", func(ctx context.Context, s *Service) error { return s.SetName(ctx, "p", "   ") }, "name"},
		{"65 rune name", func(ctx context.Context, s *Service) error { return s.SetName(ctx, "p", strings.Repeat("ş", 65)) }, "name"},
		{"name bad player", func(ctx context.Context, s *Service) error { return s.SetName(ctx, "", "x") }, "player_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call(t.Context(), svc)
			if tt.wantField == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("err = %v, want *ValidationError", err)
			}
			if verr.Field != tt.wantField {
				t.Errorf("field = %q, want %q", verr.Field, tt.wantField)
			}
		})
	}
}

func TestServicePlayerRankNotFound(t *testing.T) {
	svc := NewService(NewMemoryStore())
	_, err := svc.PlayerRank(t.Context(), "b", "ghost")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
