package user

import (
	"fmt"
	"time"
)

// OAuthProvider represents supported OAuth providers
type OAuthProvider string

const (
	OAuthProviderGoogle OAuthProvider = "google"
)

// OAuthIdentity stores the mapping between an OAuth provider identity and an internal user
type OAuthIdentity struct {
	ID            string        `json:"id"`              // "google:sub_id"
	Provider      OAuthProvider `json:"provider"`        // OAuth provider name
	Subject       string        `json:"subject"`         // Provider's unique identifier (e.g., Google's sub claim)
	Email         string        `json:"email"`           // Email from OAuth provider
	EmailVerified bool          `json:"email_verified"`  // Whether email was verified by provider
	Name          string        `json:"name"`            // Display name from provider
	Picture       string        `json:"picture"`         // Profile picture URL
	UserID        string        `json:"user_id"`         // Internal user ID
	CreatedAt     time.Time     `json:"created_at"`      // When this identity was linked
	LastLoginAt   time.Time     `json:"last_login_at"`   // Last OAuth login time
}

// NewOAuthIdentity creates a new OAuth identity record
func NewOAuthIdentity(provider OAuthProvider, subject, email, name, picture, userID string, emailVerified bool) *OAuthIdentity {
	return &OAuthIdentity{
		ID:            fmt.Sprintf("%s:%s", provider, subject),
		Provider:      provider,
		Subject:       subject,
		Email:         email,
		EmailVerified: emailVerified,
		Name:          name,
		Picture:       picture,
		UserID:        userID,
		CreatedAt:     time.Now(),
		LastLoginAt:   time.Now(),
	}
}

// UpdateLastLogin updates the last login timestamp
func (o *OAuthIdentity) UpdateLastLogin() {
	o.LastLoginAt = time.Now()
}

// OAuthState stores temporary state for the OAuth/OIDC flow (CSRF protection).
// AccountID and Nonce were added for per-tenant OIDC: AccountID binds the state
// to the connection that issued it, and Nonce is compared against the verified
// id_token's nonce claim to defend against token replay/injection.
type OAuthState struct {
	State       string    `json:"state"`                  // Random state token (also the store key)
	RedirectURI string    `json:"redirect_uri"`           // Where to redirect after login
	CreatedAt   time.Time `json:"created_at"`             // When state was created
	InviteToken string    `json:"invite_token,omitempty"` // Optional invitation token
	AccountID   string    `json:"account_id,omitempty"`   // Account whose connection issued this state
	Nonce       string    `json:"nonce,omitempty"`        // OIDC nonce, checked against id_token
}

// NewOAuthState creates a new OAuth state record (legacy, no nonce/account).
func NewOAuthState(state, redirectURI, inviteToken string) *OAuthState {
	return &OAuthState{
		State:       state,
		RedirectURI: redirectURI,
		CreatedAt:   time.Now(),
		InviteToken: inviteToken,
	}
}

// NewOIDCState creates a new OIDC state record bound to an account and nonce.
func NewOIDCState(state, accountID, nonce, redirectURI, inviteToken string) *OAuthState {
	return &OAuthState{
		State:       state,
		RedirectURI: redirectURI,
		CreatedAt:   time.Now(),
		InviteToken: inviteToken,
		AccountID:   accountID,
		Nonce:       nonce,
	}
}

// IsExpired checks if the state has expired (based on provided TTL)
func (s *OAuthState) IsExpired(ttl time.Duration) bool {
	return time.Since(s.CreatedAt) > ttl
}
