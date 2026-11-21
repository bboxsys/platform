package ai

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bboxsys/store"
	"github.com/dgraph-io/badger/v4"
	"github.com/oklog/ulid/v2"
)

// SessionPrefix is the store prefix sessions live under, scoped by account:
// {accountID}:ai_session:{userID}:{sessionID}.
const SessionPrefix = "ai_session"

type storeSessions struct {
	st *store.Store
}

// NewStoreSessions returns a SessionStore on a bboxsys store. Sessions are
// account-scoped records keyed by owner and id, so a user's list is one
// prefix scan and nothing crosses an account.
func NewStoreSessions(st *store.Store) SessionStore {
	return &storeSessions{st: st}
}

func sessionKey(userID, id string) string { return userID + ":" + id }

// validScope mirrors the store's own check so a prefix scan built here can
// never reach past the account segment.
func validScope(accountID, userID string) error {
	if accountID == "" || strings.Contains(accountID, ":") {
		return errors.New("ai: invalid account id")
	}
	if userID == "" || strings.Contains(userID, ":") {
		return errors.New("ai: invalid user id")
	}
	return nil
}

// Put stores s, assigning a ULID when ID is empty and stamping the times.
func (ss *storeSessions) Put(_ context.Context, s *Session) error {
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
	if err := ss.st.PutScoped(s.AccountID, SessionPrefix, sessionKey(s.UserID, s.ID), s); err != nil {
		return fmt.Errorf("ai: put session %s: %w", s.ID, err)
	}
	return nil
}

// Get returns the owner's session or ErrSessionNotFound.
func (ss *storeSessions) Get(_ context.Context, accountID, userID, id string) (*Session, error) {
	if err := validScope(accountID, userID); err != nil {
		return nil, err
	}
	var s Session
	err := ss.st.GetScoped(accountID, SessionPrefix, sessionKey(userID, id), &s)
	if errors.Is(err, badger.ErrKeyNotFound) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("ai: get session %s: %w", id, err)
	}
	// The key already carries the owner; the check guards a record written
	// under one owner's key with another's body.
	if s.AccountID != accountID || s.UserID != userID {
		return nil, ErrSessionNotFound
	}
	return &s, nil
}

// List returns the owner's sessions newest first.
func (ss *storeSessions) List(_ context.Context, accountID, userID string) ([]SessionSummary, error) {
	if err := validScope(accountID, userID); err != nil {
		return nil, err
	}
	sessions := make([]Session, 0)
	prefix := store.ScopedPrefix(accountID, SessionPrefix) + userID + ":"
	if err := ss.st.Get(prefix, &sessions); err != nil {
		return nil, fmt.Errorf("ai: list sessions: %w", err)
	}
	out := make([]SessionSummary, 0, len(sessions))
	for i := range sessions {
		if sessions[i].UserID != userID {
			continue
		}
		out = append(out, sessions[i].Summary())
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// Delete removes the owner's session or returns ErrSessionNotFound.
func (ss *storeSessions) Delete(ctx context.Context, accountID, userID, id string) error {
	// Badger deletes of a missing key succeed, so existence is checked first.
	if _, err := ss.Get(ctx, accountID, userID, id); err != nil {
		return err
	}
	if err := ss.st.DeleteScoped(accountID, SessionPrefix, sessionKey(userID, id)); err != nil {
		return fmt.Errorf("ai: delete session %s: %w", id, err)
	}
	return nil
}
