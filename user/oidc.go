package user

import (
	"fmt"
	"strings"
	"time"
)

// GoogleIssuerURL is the OIDC issuer for Google accounts. Google is modeled as
// a preconfigured OIDC connection rather than a bespoke provider.
const GoogleIssuerURL = "https://accounts.google.com"

// LegacyGoogleProvider is the provider key that the deprecated bespoke Google
// OAuth flow used when storing federated identities ("google:{sub}"). New logins
// store identities under the issuer-based provider key (see ProviderKeyForIssuer),
// but lookups fall back to this legacy key so existing Google users keep working.
const LegacyGoogleProvider = "google"

// Default OIDC connection values.
var DefaultOIDCScopes = []string{"openid", "email", "profile", "groups"}

const (
	DefaultGroupsClaim = "groups"
	DefaultOIDCRole    = RoleUser
)

// ProviderKeyForIssuer returns the federated-identity provider key for an OIDC
// issuer. The key namespaces every issuer under "oidc:" so identities from
// different IdPs never collide (e.g. "oidc:https://accounts.google.com").
func ProviderKeyForIssuer(issuer string) string {
	return "oidc:" + strings.TrimSpace(issuer)
}

// OIDCConnection is a per-tenant OpenID Connect login connection. Exactly one
// connection may exist per account. The Slug provides a stable, per-tenant
// login route (/api/v1/oidc/{slug}/authorize).
type OIDCConnection struct {
	AccountID    string `json:"account_id"`
	Slug         string `json:"slug"`     // unique per-tenant login slug
	Enabled      bool   `json:"enabled"`  // whether login via this connection is allowed
	IssuerURL    string `json:"issuer_url"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"` // NEVER returned by any GET handler or logged
	RedirectURI  string `json:"redirect_uri"`

	Scopes      []string `json:"scopes"`       // default: openid,email,profile,groups
	GroupsClaim string   `json:"groups_claim"` // id_token claim carrying group membership; default "groups"

	// GroupRoleMappings maps an IdP group name to one of user.AllRoles(). The
	// IdP is authoritative: roles are re-synced from these mappings on every login.
	GroupRoleMappings map[string]string `json:"group_role_mappings"`

	AutoProvision bool     `json:"auto_provision"` // JIT-create users on first login; default true
	DefaultRoles  []string `json:"default_roles"`  // roles when no group maps; default ["user"]

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewOIDCConnection builds a connection, applying defaults for any unset
// optional fields. It does not validate the mutable fields; callers should use
// Validate before persisting.
func NewOIDCConnection(accountID, slug, issuerURL, clientID, clientSecret, redirectURI string) *OIDCConnection {
	now := time.Now()
	c := &OIDCConnection{
		AccountID:         accountID,
		Slug:              slug,
		Enabled:           true,
		IssuerURL:         issuerURL,
		ClientID:          clientID,
		ClientSecret:      clientSecret,
		RedirectURI:       redirectURI,
		Scopes:            append([]string(nil), DefaultOIDCScopes...),
		GroupsClaim:       DefaultGroupsClaim,
		GroupRoleMappings: map[string]string{},
		AutoProvision:     true,
		DefaultRoles:      []string{DefaultOIDCRole},
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	return c
}

// ApplyDefaults fills in defaults for optional fields left empty by an API
// caller. It is idempotent.
func (c *OIDCConnection) ApplyDefaults() {
	if len(c.Scopes) == 0 {
		c.Scopes = append([]string(nil), DefaultOIDCScopes...)
	}
	if c.GroupsClaim == "" {
		c.GroupsClaim = DefaultGroupsClaim
	}
	if c.GroupRoleMappings == nil {
		c.GroupRoleMappings = map[string]string{}
	}
	if len(c.DefaultRoles) == 0 {
		c.DefaultRoles = []string{DefaultOIDCRole}
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
	}
	c.UpdatedAt = time.Now()
}

// Validate checks the connection's required fields, transport security, and
// that every mapped/default role is a known canonical role.
func (c *OIDCConnection) Validate() error {
	if strings.TrimSpace(c.Slug) == "" {
		return fmt.Errorf("oidc connection: slug is required")
	}
	if strings.ContainsAny(c.Slug, ":/ ") {
		return fmt.Errorf("oidc connection: slug %q must not contain ':', '/', or spaces", c.Slug)
	}
	if strings.TrimSpace(c.ClientID) == "" {
		return fmt.Errorf("oidc connection: client_id is required")
	}
	if err := requireHTTPS(c.IssuerURL, "issuer_url"); err != nil {
		return err
	}
	if err := requireHTTPS(c.RedirectURI, "redirect_uri"); err != nil {
		return err
	}
	for group, role := range c.GroupRoleMappings {
		if !IsValidRole(role) {
			return fmt.Errorf("oidc connection: group %q maps to unknown role %q", group, role)
		}
	}
	for _, role := range c.DefaultRoles {
		if !IsValidRole(role) {
			return fmt.Errorf("oidc connection: default role %q is not a valid role", role)
		}
	}
	return nil
}

// requireHTTPS enforces that an endpoint is https, allowing http only for
// localhost/127.0.0.1 during development.
func requireHTTPS(raw, field string) error {
	v := strings.TrimSpace(raw)
	if v == "" {
		return fmt.Errorf("oidc connection: %s is required", field)
	}
	if strings.HasPrefix(v, "https://") {
		return nil
	}
	if strings.HasPrefix(v, "http://localhost") || strings.HasPrefix(v, "http://127.0.0.1") {
		return nil
	}
	return fmt.Errorf("oidc connection: %s must use https (http allowed only for localhost)", field)
}

// PublicOIDCConnection is the secret-free projection of a connection returned
// by GET handlers. It deliberately omits ClientSecret.
type PublicOIDCConnection struct {
	AccountID         string            `json:"account_id"`
	Slug              string            `json:"slug"`
	Enabled           bool              `json:"enabled"`
	IssuerURL         string            `json:"issuer_url"`
	ClientID          string            `json:"client_id"`
	RedirectURI       string            `json:"redirect_uri"`
	Scopes            []string          `json:"scopes"`
	GroupsClaim       string            `json:"groups_claim"`
	GroupRoleMappings map[string]string `json:"group_role_mappings"`
	AutoProvision     bool              `json:"auto_provision"`
	DefaultRoles      []string          `json:"default_roles"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

// Public returns the connection without its ClientSecret, safe to serialize to
// clients.
func (c *OIDCConnection) Public() *PublicOIDCConnection {
	return &PublicOIDCConnection{
		AccountID:         c.AccountID,
		Slug:              c.Slug,
		Enabled:           c.Enabled,
		IssuerURL:         c.IssuerURL,
		ClientID:          c.ClientID,
		RedirectURI:       c.RedirectURI,
		Scopes:            c.Scopes,
		GroupsClaim:       c.GroupsClaim,
		GroupRoleMappings: c.GroupRoleMappings,
		AutoProvision:     c.AutoProvision,
		DefaultRoles:      c.DefaultRoles,
		CreatedAt:         c.CreatedAt,
		UpdatedAt:         c.UpdatedAt,
	}
}

// NewFederatedIdentity creates an OAuthIdentity for a federated (OIDC) login.
// providerKey is an issuer-based key (see ProviderKeyForIssuer); it is stored on
// the identity so the same struct serves both the legacy Google flow and generic
// OIDC.
func NewFederatedIdentity(providerKey, subject, email, name, picture, userID string, emailVerified bool) *OAuthIdentity {
	now := time.Now()
	return &OAuthIdentity{
		ID:            fmt.Sprintf("%s:%s", providerKey, subject),
		Provider:      OAuthProvider(providerKey),
		Subject:       subject,
		Email:         email,
		EmailVerified: emailVerified,
		Name:          name,
		Picture:       picture,
		UserID:        userID,
		CreatedAt:     now,
		LastLoginAt:   now,
	}
}
