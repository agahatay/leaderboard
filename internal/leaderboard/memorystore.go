package leaderboard

import (
	"cmp"
	"context"
	"slices"
	"sync"
)

// MemoryStore is an in-memory Store. It is meant for tests and local runs;
// data is lost when the process exits.
type MemoryStore struct {
	mu     sync.RWMutex
	boards map[string]map[string]int64 // board -> player -> score
	names  map[string]string           // player -> name
}

// Compile-time check that MemoryStore implements Store.
var _ Store = (*MemoryStore)(nil)

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		boards: make(map[string]map[string]int64),
		names:  make(map[string]string),
	}
}

// ranksAbove reports whether (idA, scoreA) is placed before (idB, scoreB):
// higher score first, then higher player ID (Redis REV order).
func ranksAbove(idA string, scoreA int64, idB string, scoreB int64) bool {
	if scoreA != scoreB {
		return scoreA > scoreB
	}
	return idA > idB
}

func (m *MemoryStore) SubmitScore(ctx context.Context, board, playerID string, score int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	scores, ok := m.boards[board]
	if !ok {
		scores = make(map[string]int64)
		m.boards[board] = scores
	}
	if current, exists := scores[playerID]; exists && score <= current {
		return nil
	}
	scores[playerID] = score
	return nil
}

func (m *MemoryStore) Top(ctx context.Context, board string, n int) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	scores := m.boards[board] // nil map is fine to range over
	entries := make([]Entry, 0, len(scores))
	for id, score := range scores {
		entries = append(entries, Entry{PlayerID: id, Score: score})
	}
	slices.SortFunc(entries, func(a, b Entry) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		return cmp.Compare(b.PlayerID, a.PlayerID)
	})
	if len(entries) > n {
		entries = entries[:n]
	}
	for i := range entries {
		entries[i].Rank = int64(i + 1)
		entries[i].Name = m.names[entries[i].PlayerID]
	}
	return entries, nil
}

func (m *MemoryStore) PlayerRank(ctx context.Context, board, playerID string) (PlayerRank, error) {
	if err := ctx.Err(); err != nil {
		return PlayerRank{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	scores := m.boards[board]
	score, ok := scores[playerID]
	if !ok {
		return PlayerRank{}, ErrNotFound
	}
	rank := int64(1)
	for id, s := range scores {
		if ranksAbove(id, s, playerID, score) {
			rank++
		}
	}
	return PlayerRank{Rank: rank, Score: score}, nil
}

func (m *MemoryStore) SetName(ctx context.Context, playerID, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.names[playerID] = name
	return nil
}
