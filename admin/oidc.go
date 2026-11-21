package admin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/bboxsys/platform/user"
)

var (
	// ErrOIDCStateInvalid is returned when the callback state is unknown (never
	// issued, already consumed, or forged).
	ErrOIDCStateInvalid = errors.New("invalid oidc state")
	// ErrOIDCStateExpired is returned when the state exceeded its TTL.
	ErrOIDCStateExpired = errors.New("oidc state has expired")
	// ErrOIDCNoIDToken is returned when the token response carried no id_token.
	ErrOIDCNoIDToken = errors.New("oidc token response did not contain an id_token")
	// ErrOIDCIDTokenVerify is returned when id_token verification fails (bad
	// signature, issuer, audience, or expiry).
	ErrOIDCIDTokenVerify = errors.New("id_token verification failed")
	// ErrOIDCNonceMismatch is returned when the id_token nonce does not match the
	// nonce bound to the single-use state.
	ErrOIDCNonceMismatch = errors.New("id_token nonce does not match")
)

// OIDCClaims holds the identity-bearing claims extracted from a VERIFIED
// id_token. It is the sole source of truth for a federated login's identity and
// group membership; userinfo may enrich display fields but is never trusted for
// identity or groups.
type OIDCClaims struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	Picture       string
	Groups        []string
}

// OIDCProvider builds and caches go-oidc providers and drives the
// authorization-code + id_token-verification flow for per-tenant connections.
type OIDCProvider struct {
	st       *Store
	stateTTL time.Duration

	mu    sync.Mutex
	cache map[string]*oidc.Provider // keyed by issuer URL
}

// NewOIDCProvider constructs an OIDCProvider. stateTTL bounds how long an
// authorization request's state/nonce remain valid.
func NewOIDCProvider(st *Store, stateTTL time.Duration) *OIDCProvider {
	if stateTTL <= 0 {
		stateTTL = 10 * time.Minute
	}
	return &OIDCProvider{
		st:       st,
		stateTTL: stateTTL,
		cache:    make(map[string]*oidc.Provider),
	}
}

// provider returns a discovered go-oidc provider for the connection's issuer,
// caching by issuer URL (the discovery document depends only on the issuer, not
// on per-tenant credentials).
func (p *OIDCProvider) provider(ctx context.Context, conn *user.OIDCConnection) (*oidc.Provider, error) {
	p.mu.Lock()
	cached, ok := p.cache[conn.IssuerURL]
	p.mu.Unlock()
	if ok {
		return cached, nil
	}

	prov, err := oidc.NewProvider(ctx, conn.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery failed for %s: %w", conn.IssuerURL, err)
	}

	p.mu.Lock()
	p.cache[conn.IssuerURL] = prov
	p.mu.Unlock()
	return prov, nil
}

// invalidate drops any cached provider for an issuer, forcing rediscovery on the
// next use. Called after a connection's config changes.
func (p *OIDCProvider) invalidate(issuerURL string) {
	p.mu.Lock()
	delete(p.cache, issuerURL)
	p.mu.Unlock()
}

// oauth2Config assembles the oauth2 config for a connection from the discovered
// provider endpoints and the connection's credentials/scopes.
func (p *OIDCProvider) oauth2Config(conn *user.OIDCConnection, prov *oidc.Provider) *oauth2.Config {
	scopes := conn.Scopes
	if len(scopes) == 0 {
		scopes = user.DefaultOIDCScopes
	}
	return &oauth2.Config{
		ClientID:     conn.ClientID,
		ClientSecret: conn.ClientSecret,
		RedirectURL:  conn.RedirectURI,
		Endpoint:     prov.Endpoint(),
		Scopes:       scopes,
	}
}

// AuthorizationURL generates a crypto-random state and nonce, persists a
// single-use state record (bound to the connection's account, the nonce, any
// invite token, and the post-login return path), and returns the IdP
// authorization URL carrying state + nonce. returnTo is the same-origin SPA path
// the callback should send the browser to after login; it is stored on the state
// so the redirect is keyed off the single-use state, not off client input at
// callback time.
func (p *OIDCProvider) AuthorizationURL(ctx context.Context, conn *user.OIDCConnection, inviteToken, returnTo string) (string, string, error) {
	prov, err := p.provider(ctx, conn)
	if err != nil {
		return "", "", err
	}

	state, err := randomToken()
	if err != nil {
		return "", "", err
	}
	nonce, err := randomToken()
	if err != nil {
		return "", "", err
	}

	stateRecord := user.NewOIDCState(state, conn.AccountID, nonce, safeReturnTo(returnTo), inviteToken)
	if err := p.st.PutOAuthState(stateRecord); err != nil {
		return "", "", err
	}

	cfg := p.oauth2Config(conn, prov)
	return cfg.AuthCodeURL(state, oidc.Nonce(nonce)), state, nil
}

// safeReturnTo constrains the post-login redirect to a same-origin relative
// path, defending against open-redirect. Anything else falls back to "/".
func safeReturnTo(returnTo string) string {
	if len(returnTo) < 1 || returnTo[0] != '/' {
		return "/"
	}
	// Reject protocol-relative ("//host") and back-slash tricks.
	if len(returnTo) >= 2 && (returnTo[1] == '/' || returnTo[1] == '\\') {
		return "/"
	}
	return returnTo
}

// ExchangeAndVerify consumes the single-use state, exchanges the authorization
// code, and VERIFIES the returned id_token (signature + iss/aud/exp) and its
// nonce against the state. On success it returns the verified claims and the
// state record (for invite/redirect handling).
func (p *OIDCProvider) ExchangeAndVerify(ctx context.Context, conn *user.OIDCConnection, code, state string) (*OIDCClaims, *user.OAuthState, error) {
	// Load and immediately delete the state so it can be used at most once,
	// even if any later step fails.
	stateRecord, err := p.st.GetOAuthState(state)
	if err != nil {
		return nil, nil, ErrOIDCStateInvalid
	}
	p.st.DeleteOAuthState(state)

	if stateRecord.IsExpired(p.stateTTL) {
		return nil, nil, ErrOIDCStateExpired
	}

	prov, err := p.provider(ctx, conn)
	if err != nil {
		return nil, nil, err
	}
	cfg := p.oauth2Config(conn, prov)

	token, err := cfg.Exchange(ctx, code)
	if err != nil {
		return nil, nil, fmt.Errorf("oidc code exchange failed: %w", err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return nil, nil, ErrOIDCNoIDToken
	}

	verifier := prov.Verifier(&oidc.Config{ClientID: conn.ClientID})
	idToken, err := verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrOIDCIDTokenVerify, err)
	}

	// The nonce in the verified id_token MUST match the nonce we bound to the
	// single-use state, defeating token replay/injection.
	if idToken.Nonce != stateRecord.Nonce {
		return nil, nil, ErrOIDCNonceMismatch
	}

	claims, err := claimsFromIDToken(idToken, conn.GroupsClaim)
	if err != nil {
		return nil, nil, err
	}
	// idToken.Subject is the verified subject; prefer it over any claim copy.
	if idToken.Subject != "" {
		claims.Subject = idToken.Subject
	}

	return claims, stateRecord, nil
}

// standardClaims mirrors the well-known OIDC claims we consume for identity and
// display. Groups are read separately because the claim name is configurable.
type standardClaims struct {
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
}

// claimsFromIDToken parses the verified id_token's claims, extracting the
// standard identity fields plus the configurable groups claim.
func claimsFromIDToken(idToken *oidc.IDToken, groupsClaim string) (*OIDCClaims, error) {
	var raw map[string]json.RawMessage
	if err := idToken.Claims(&raw); err != nil {
		return nil, fmt.Errorf("failed to parse id_token claims: %w", err)
	}

	var std standardClaims
	if b, ok := raw["sub"]; ok {
		_ = json.Unmarshal(b, &std.Subject)
	}
	if b, ok := raw["email"]; ok {
		_ = json.Unmarshal(b, &std.Email)
	}
	if b, ok := raw["email_verified"]; ok {
		// email_verified may be a bool or a string in some IdPs.
		if err := json.Unmarshal(b, &std.EmailVerified); err != nil {
			var s string
			if json.Unmarshal(b, &s) == nil {
				std.EmailVerified = s == "true"
			}
		}
	}
	if b, ok := raw["name"]; ok {
		_ = json.Unmarshal(b, &std.Name)
	}
	if b, ok := raw["picture"]; ok {
		_ = json.Unmarshal(b, &std.Picture)
	}

	claimName := groupsClaim
	if claimName == "" {
		claimName = user.DefaultGroupsClaim
	}

	return &OIDCClaims{
		Subject:       std.Subject,
		Email:         std.Email,
		EmailVerified: std.EmailVerified,
		Name:          std.Name,
		Picture:       std.Picture,
		Groups:        extractGroups(raw, claimName),
	}, nil
}

// extractGroups reads the configured groups claim as a list of strings. It
// tolerates both a JSON array and a single string value.
func extractGroups(raw map[string]json.RawMessage, claimName string) []string {
	b, ok := raw[claimName]
	if !ok {
		return nil
	}
	var list []string
	if err := json.Unmarshal(b, &list); err == nil {
		return list
	}
	var single string
	if err := json.Unmarshal(b, &single); err == nil && single != "" {
		return []string{single}
	}
	return nil
}

// rolesFromGroups maps a set of IdP groups to platform roles using the
// connection's GroupRoleMappings. Results are deduplicated and restricted to
// user.AllRoles(); if no group maps to a known role, the connection's
// DefaultRoles are used. The IdP is authoritative, so this is applied on every
// login.
func rolesFromGroups(conn *user.OIDCConnection, groups []string) []string {
	valid := make(map[string]bool, len(user.AllRoles()))
	for _, r := range user.AllRoles() {
		valid[r] = true
	}

	seen := make(map[string]bool)
	roles := make([]string, 0)
	for _, g := range groups {
		role, ok := conn.GroupRoleMappings[g]
		if !ok || !valid[role] || seen[role] {
			continue
		}
		seen[role] = true
		roles = append(roles, role)
	}

	if len(roles) == 0 {
		for _, r := range conn.DefaultRoles {
			if valid[r] && !seen[r] {
				seen[r] = true
				roles = append(roles, r)
			}
		}
	}
	return roles
}

// randomToken returns a cryptographically-random URL-safe token used for state
// and nonce values.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
