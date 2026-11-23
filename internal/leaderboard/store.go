package leaderboard

import "context"

// Store persists scores and player names. Implementations must be safe for
// concurrent use. Inputs are already validated by Service.
type Store interface {
	// SubmitScore records score for playerID on board, but only if it is
	// higher than the player's current score ("best score" mode).
	SubmitScore(ctx context.Context, board, playerID string, score int64) error

	// SubmitScores applies many SubmitScore calls at once, with the same
	// best-score rule; it is used by the async ingest path. Implementations
	// must not keep a reference to events after returning.
	SubmitScores(ctx context.Context, events []ScoreEvent) error

	// Top returns up to n entries, highest score first. Equal scores are
	// ordered by player ID, descending (same as Redis ZRANGE ... REV).
	// An unknown board yields an empty, non-nil slice.
	Top(ctx context.Context, board string, n int) ([]Entry, error)

	// PlayerRank returns the player's rank and score, or ErrNotFound.
	PlayerRank(ctx context.Context, board, playerID string) (PlayerRank, error)

	// Around returns the player's entry with up to radius entries above and
	// below it, clamped at the ends of the board, or ErrNotFound.
	Around(ctx context.Context, board, playerID string, radius int) ([]Entry, error)

	// SetName sets the display name of a player across all boards.
	SetName(ctx context.Context, playerID, name string) error
}
