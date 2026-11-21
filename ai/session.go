package ai

import (
	"context"
	"errors"
	"time"
)

// Session is one conversation. Messages are append-only.
type Session struct {
	ID        string `json:"id"`
	AccountID string `json:"account_id"`
	// UserID is the owner's NATS public key.
	UserID string `json:"user_id"`
	Title  string `json:"title,omitempty"`
	// Provider and Model record what produced Messages; a session is not
	// moved between providers.
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Credential is the vault name of the model credential in use.
	Credential string    `json:"credential,omitempty"`
	Messages   []Message `json:"messages"`
	// Usage is cumulative over every turn.
	Usage     Usage     `json:"usage"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Summary projects a session for lists.
func (s *Session) Summary() SessionSummary {
	return SessionSummary{
		ID:           s.ID,
		Title:        s.Title,
		Provider:     s.Provider,
		Model:        s.Model,
		MessageCount: len(s.Messages),
		Usage:        s.Usage,
		CreatedAt:    s.CreatedAt,
		UpdatedAt:    s.UpdatedAt,
	}
}

// SessionSummary is a session without its messages.
type SessionSummary struct {
	ID           string    `json:"id"`
	Title        string    `json:"title,omitempty"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	MessageCount int       `json:"message_count"`
	Usage        Usage     `json:"usage"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ErrSessionNotFound is returned by SessionStore.Get and Delete.
var ErrSessionNotFound = errors.New("ai: session not found")

// SessionStore persists sessions scoped by account and owner.
type SessionStore interface {
	Put(ctx context.Context, s *Session) error
	Get(ctx context.Context, accountID, userID, id string) (*Session, error)
	// List returns the owner's sessions, newest first.
	List(ctx context.Context, accountID, userID string) ([]SessionSummary, error)
	Delete(ctx context.Context, accountID, userID, id string) error
}
