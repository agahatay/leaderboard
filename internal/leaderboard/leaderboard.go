// Package leaderboard contains the domain types, validation rules, the Store
// interface and an in-memory Store implementation.
package leaderboard

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxScore is the highest accepted score (1e12).
const MaxScore int64 = 1_000_000_000_000

// Limits enforced by the domain.
const (
	DefaultLimit  = 10
	MaxLimit      = 100
	DefaultRadius = 5
	MaxRadius     = 50
	MaxNameLen    = 64
)

// ScoreEvent is one score submission.
type ScoreEvent struct {
	Board    string
	PlayerID string
	Score    int64
}

// ValidateScoreEvent checks board, player and score. The async write path
// calls it before queueing, so invalid input still gets a 400.
func ValidateScoreEvent(ev ScoreEvent) error {
	if err := validateID("board", ev.Board); err != nil {
		return err
	}
	if err := validateID("player_id", ev.PlayerID); err != nil {
		return err
	}
	return validateScore(ev.Score)
}

// ErrNotFound is returned when a player has no score on a board.
var ErrNotFound = errors.New("not found")

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// Entry is one row of a leaderboard listing. Rank is 1-based.
type Entry struct {
	Rank     int64  `json:"rank"`
	PlayerID string `json:"player_id"`
	Name     string `json:"name"`
	Score    int64  `json:"score"`
}

// PlayerRank is a player's position on one board. Rank is 1-based.
type PlayerRank struct {
	Rank  int64 `json:"rank"`
	Score int64 `json:"score"`
}

// ValidationError reports invalid input. Its message is safe to show to clients.
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string {
	return e.Field + " " + e.Msg
}

func validateID(field, id string) error {
	if !idPattern.MatchString(id) {
		return &ValidationError{Field: field, Msg: "must match " + idPattern.String()}
	}
	return nil
}

func validateScore(score int64) error {
	if score < 0 || score > MaxScore {
		return &ValidationError{Field: "score", Msg: fmt.Sprintf("must be an integer between 0 and %d", MaxScore)}
	}
	return nil
}

func validateLimit(limit int) error {
	if limit < 1 || limit > MaxLimit {
		return &ValidationError{Field: "limit", Msg: fmt.Sprintf("must be between 1 and %d", MaxLimit)}
	}
	return nil
}

func validateRadius(radius int) error {
	if radius < 1 || radius > MaxRadius {
		return &ValidationError{Field: "radius", Msg: fmt.Sprintf("must be between 1 and %d", MaxRadius)}
	}
	return nil
}

func validateName(name string) error {
	if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > MaxNameLen {
		return &ValidationError{Field: "name", Msg: fmt.Sprintf("must be 1 to %d characters and not blank", MaxNameLen)}
	}
	return nil
}
