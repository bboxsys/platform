package admin

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// APITokenType represents the type of API token/credential
type APITokenType string

const (
	APITokenTypeCloudflare    APITokenType = "cloudflare"
	APITokenTypeNavionics     APITokenType = "navionics"
	APITokenTypeThunderforest APITokenType = "thunderforest"
	APITokenTypeOpenStreetMap APITokenType = "openstreetmap"
	// APITokenTypeBearer is a product-neutral bearer token for an HTTP API
	// the backend calls on the user's behalf. Pair it with Metadata to say
	// which resource and endpoint it belongs to.
	APITokenTypeBearer APITokenType = "bearer"
	APITokenTypeCustom APITokenType = "custom"
)

// APITokenScope controls who in an account can see and use a token.
type APITokenScope string

const (
	// APITokenScopeUser means only the owning user can see or use the token.
	APITokenScopeUser APITokenScope = "user"
	// APITokenScopeAccount means every user in the account can see and use
	// it. Only administrators may create, update, or delete one.
	APITokenScopeAccount APITokenScope = "account"
)

// APITokenAccountOwner is the reserved owner segment in the storage key of
// account-scoped tokens. User-scoped keys carry the owner's NATS public key
// (56 characters, prefix U), so it cannot collide with a real owner; the
// store still refuses to write a user-scoped token under this owner.
const APITokenAccountOwner = "_account"

// APITokenMaskedSecret is the placeholder older clients echo back on update
// in place of the secret. The server treats it as "leave the secret alone".
const APITokenMaskedSecret = "********"

// maxAPITokenNameLength bounds the name, which is both a storage key segment
// and a URL path segment.
const maxAPITokenNameLength = 128

var (
	ErrInvalidAPITokenName  = errors.New("invalid api token name")
	ErrInvalidAPITokenType  = errors.New("invalid api token type")
	ErrInvalidAPITokenScope = errors.New("invalid api token scope")
	ErrReservedAPITokenUser = errors.New("api token owner is reserved")
)

// APITokenMetadata associates a token with the resource it authenticates
// against. All fields are optional and opaque to the platform; the backend
// that uses the credential decides what they mean.
type APITokenMetadata struct {
	ResourceType string            `json:"resource_type,omitempty"` // e.g. "example_deployment"
	ResourceID   string            `json:"resource_id,omitempty"`   // the resource's own identifier
	ResourceName string            `json:"resource_name,omitempty"` // display name
	Endpoint     string            `json:"endpoint,omitempty"`      // base URL the backend calls with the token
	Tags         map[string]string `json:"tags,omitempty"`
}

// IsZero reports whether no association was supplied. An update with zero
// metadata leaves the stored metadata alone.
func (m APITokenMetadata) IsZero() bool {
	return m.ResourceType == "" && m.ResourceID == "" && m.ResourceName == "" && m.Endpoint == "" && m.Tags == nil
}

// APIToken is a stored API credential. The caller supplies the secret, the
// platform stores it encrypted, and no API path ever returns it: handlers
// respond with APITokenView. APIToken is for the store and for backend code
// that uses the credential.
type APIToken struct {
	Name      string           `json:"name"`
	Type      APITokenType     `json:"type"`
	Scope     APITokenScope    `json:"scope"`
	ID        string           `json:"id"`    // Varies by type: email, client ID, account ID, etc.
	Token     string           `json:"token"` // The secret: password, API key, client secret, etc.
	Metadata  APITokenMetadata `json:"metadata"`
	UserID    string           `json:"user_id"` // Creator (user scope: owner). The NATS public key.
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// APITokenView is the only shape the API returns. It has no secret field, so
// a handler cannot leak the token by forgetting to mask it.
type APITokenView struct {
	Name      string           `json:"name"`
	Type      APITokenType     `json:"type"`
	Scope     APITokenScope    `json:"scope"`
	ID        string           `json:"id"`
	HasToken  bool             `json:"has_token"`
	Metadata  APITokenMetadata `json:"metadata"`
	UserID    string           `json:"user_id"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// NewAPIToken builds a token from a request body. userID is the creator; the
// request's UserID and timestamps are ignored. Scope defaults to user.
func NewAPIToken(req APIToken, userID string) *APIToken {
	now := time.Now()
	t := req
	t.UserID = userID
	t.CreatedAt = now
	t.UpdatedAt = now
	t.Normalize()
	return &t
}

// Normalize fills defaults so no caller ever sees an empty scope. Records
// written before scope existed unmarshal with Scope == "" and become
// user-scoped, which keeps them private to their owner.
func (t *APIToken) Normalize() {
	if t.Scope == "" {
		t.Scope = APITokenScopeUser
	}
}

// View returns the secret-free shape handlers respond with.
func (t *APIToken) View() APITokenView {
	t.Normalize()
	return APITokenView{
		Name:      t.Name,
		Type:      t.Type,
		Scope:     t.Scope,
		ID:        t.ID,
		HasToken:  t.Token != "",
		Metadata:  t.Metadata,
		UserID:    t.UserID,
		CreatedAt: t.CreatedAt,
		UpdatedAt: t.UpdatedAt,
	}
}

// APITokenViews converts a slice of tokens for a list response. It always returns a
// non-nil slice so an empty list encodes as [] rather than null.
func APITokenViews(tokens []APIToken) []APITokenView {
	views := make([]APITokenView, 0, len(tokens))
	for i := range tokens {
		views = append(views, tokens[i].View())
	}
	return views
}

// Validate checks the fields a client controls. It does not check the
// secret, since an update may legitimately omit it.
func (t *APIToken) Validate() error {
	if !ValidateAPITokenName(t.Name) {
		return ErrInvalidAPITokenName
	}
	if !ValidateAPITokenType(string(t.Type)) {
		return ErrInvalidAPITokenType
	}
	if !ValidateAPITokenScope(string(t.Scope)) {
		return ErrInvalidAPITokenScope
	}
	return nil
}

// storeKey is the key under the account's "apitoken" prefix: {owner}:{name},
// where owner is the user's public key for user scope and
// APITokenAccountOwner for account scope. User-scoped keys are byte-identical
// to the pre-scope key scheme, so existing records need no migration.
func (t *APIToken) storeKey() (string, error) {
	t.Normalize()
	return apiTokenKey(t.Scope, t.UserID, t.Name)
}

func apiTokenKey(scope APITokenScope, userID, name string) (string, error) {
	if scope == APITokenScopeAccount {
		return fmt.Sprintf("%s:%s", APITokenAccountOwner, name), nil
	}
	if userID == "" || userID == APITokenAccountOwner {
		return "", ErrReservedAPITokenUser
	}
	return fmt.Sprintf("%s:%s", userID, name), nil
}

// ValidateAPITokenName reports whether name can be a key and path segment.
func ValidateAPITokenName(name string) bool {
	if name == "" || len(name) > maxAPITokenNameLength {
		return false
	}
	if strings.ContainsAny(name, ":/\\?#") {
		return false
	}
	return strings.TrimSpace(name) == name
}

// ValidateAPITokenType checks if the token type is valid
func ValidateAPITokenType(t string) bool {
	switch APITokenType(t) {
	case APITokenTypeCloudflare,
		APITokenTypeNavionics,
		APITokenTypeThunderforest,
		APITokenTypeOpenStreetMap,
		APITokenTypeBearer,
		APITokenTypeCustom:
		return true
	}
	return false
}

// ValidateAPITokenScope checks if the scope is one of the known values.
func ValidateAPITokenScope(s string) bool {
	switch APITokenScope(s) {
	case APITokenScopeUser, APITokenScopeAccount:
		return true
	}
	return false
}
