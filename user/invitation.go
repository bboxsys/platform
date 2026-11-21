package user

import (
	"crypto/rand"
	"encoding/base64"
	"time"
)

// InvitationStatus represents the current state of an invitation
type InvitationStatus string

const (
	InvitationPending  InvitationStatus = "pending"
	InvitationAccepted InvitationStatus = "accepted"
	InvitationExpired  InvitationStatus = "expired"
	InvitationRevoked  InvitationStatus = "revoked"
)

// Invitation represents an invitation for a user to join an account
type Invitation struct {
	ID         string           `json:"id"`                     // Unique identifier
	Token      string           `json:"token"`                  // URL-safe token for invitation link
	AccountID  string           `json:"account_id"`             // Account to join
	InvitedBy  string           `json:"invited_by"`             // UserID of inviter
	Email      string           `json:"email"`                  // Email address of invitee
	Roles      []string         `json:"roles"`                  // Roles to assign on acceptance
	Status     InvitationStatus `json:"status"`                 // Current status
	CreatedAt  time.Time        `json:"created_at"`             // When invitation was created
	ExpiresAt  time.Time        `json:"expires_at"`             // When invitation expires
	AcceptedAt *time.Time       `json:"accepted_at,omitempty"`  // When invitation was accepted
	AcceptedBy string           `json:"accepted_by,omitempty"`  // UserID who accepted
}

// InvitationConfig contains parameters for creating an invitation
type InvitationConfig struct {
	Email     string   `json:"email"`      // Email of user to invite
	Roles     []string `json:"roles"`      // Roles to assign
	TTLHours  int      `json:"ttl_hours"`  // Time to live in hours
	AccountID string   `json:"account_id"` // Target account (optional, defaults to caller's account)
}

// NewInvitation creates a new invitation
func NewInvitation(config *InvitationConfig, accountID, invitedBy string) (*Invitation, error) {
	token, err := generateSecureToken(32)
	if err != nil {
		return nil, err
	}

	id, err := generateSecureToken(16)
	if err != nil {
		return nil, err
	}

	ttlHours := config.TTLHours
	if ttlHours <= 0 {
		ttlHours = 72 // Default 72 hours
	}

	return &Invitation{
		ID:        id,
		Token:     token,
		AccountID: accountID,
		InvitedBy: invitedBy,
		Email:     config.Email,
		Roles:     config.Roles,
		Status:    InvitationPending,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Duration(ttlHours) * time.Hour),
	}, nil
}

// IsExpired checks if the invitation has expired
func (i *Invitation) IsExpired() bool {
	return time.Now().After(i.ExpiresAt)
}

// IsPending checks if the invitation is still pending
func (i *Invitation) IsPending() bool {
	return i.Status == InvitationPending
}

// CanBeAccepted checks if the invitation can be accepted
func (i *Invitation) CanBeAccepted() bool {
	return i.IsPending() && !i.IsExpired()
}

// Accept marks the invitation as accepted
func (i *Invitation) Accept(userID string) {
	now := time.Now()
	i.Status = InvitationAccepted
	i.AcceptedAt = &now
	i.AcceptedBy = userID
}

// Revoke marks the invitation as revoked
func (i *Invitation) Revoke() {
	i.Status = InvitationRevoked
}

// generateSecureToken generates a URL-safe random token
func generateSecureToken(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(bytes), nil
}
