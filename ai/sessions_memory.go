package ai

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

type memorySessions struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewMemorySessions returns an in-memory SessionStore for tests. Values are
// deep-copied on the way in and out so callers cannot share state through
// the store.
func NewMemorySessions() SessionStore {
	return &memorySessions{sessions: make(map[string]*Session)}
}

func memoryKey(accountID, userID, id string) string {
	return accountID + "\x00" + userID + "\x00" + id
}

// cloneSession deep-copies through JSON, which also copies every RawMessage.
func cloneSession(s *Session) (*Session, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var out Session
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (ms *memorySessions) Put(_ context.Context, s *Session) error {
	if s == nil {
		return errors.New("ai: session is required")
	}
	if err := validScope(s.AccountID, s.UserID); err != nil {
		return err
	}
	now := time.Now().UTC()
	if s.ID == "" {
		s.ID = ulid.Make().String()
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	s.UpdatedAt = now
	c, err := cloneSession(s)
	if err != nil {
		return err
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.sessions[memoryKey(s.AccountID, s.UserID, s.ID)] = c
	return nil
}

func (ms *memorySessions) Get(_ context.Context, accountID, userID, id string) (*Session, error) {
	ms.mu.RLock()
	s, ok := ms.sessions[memoryKey(accountID, userID, id)]
	ms.mu.RUnlock()
	if !ok {
		return nil, ErrSessionNotFound
	}
	return cloneSession(s)
}

func (ms *memorySessions) List(_ context.Context, accountID, userID string) ([]SessionSummary, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	out := make([]SessionSummary, 0)
	for _, s := range ms.sessions {
		if s.AccountID == accountID && s.UserID == userID {
			out = append(out, s.Summary())
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (ms *memorySessions) Delete(_ context.Context, accountID, userID, id string) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	k := memoryKey(accountID, userID, id)
	if _, ok := ms.sessions[k]; !ok {
		return ErrSessionNotFound
	}
	delete(ms.sessions, k)
	return nil
}
