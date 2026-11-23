// External test package: storetest imports leaderboard, so this file cannot
// live in package leaderboard without an import cycle.
package leaderboard_test

import (
	"testing"

	"github.com/agahatay/leaderboard/internal/leaderboard"
	"github.com/agahatay/leaderboard/internal/leaderboard/storetest"
)

func TestMemoryStore(t *testing.T) {
	storetest.Run(t, func(*testing.T) leaderboard.Store {
		return leaderboard.NewMemoryStore()
	})
}
