package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bboxsys/platform/oidctoken"
)

// An API token's secret is either the bearer itself or the durable OIDC
// inputs a bearer is minted from. Which one is recorded in the token's
// metadata tags, so the vault's read API (APITokenView) shows how a
// credential authenticates without ever showing what it authenticates with.
//
// The non-secret OIDC settings are tags; the client ID is the token's ID;
// the client secret, password, and refresh token are stored together as a
// JSON document (APITokenOIDCSecret) in the sealed Token field. The tag
// names and that document are the wire contract with @bboxsys/ui.
const (
	// APITokenAuthModeTag names the authentication mode. Absent or "static"
	// means Token is the bearer.
	APITokenAuthModeTag = "oidc_auth_mode"
	// APITokenOIDCIssuerTag is the OIDC issuer URL discovery is read from.
	APITokenOIDCIssuerTag = "oidc_issuer"
	// APITokenOIDCScopesTag is the space-separated scope parameter.
	APITokenOIDCScopesTag = "oidc_scopes"
	// APITokenOIDCUsernameTag is the resource owner for the password grant.
	APITokenOIDCUsernameTag = "oidc_username"
	// APITokenOIDCTokenFieldTag selects access_token (default) or id_token.
	APITokenOIDCTokenFieldTag = "oidc_token_field"
	// APITokenOIDCClientAuthMethodTag selects how the client authenticates to
	// the token endpoint; empty or "auto" discovers it.
	APITokenOIDCClientAuthMethodTag = "oidc_client_auth_method"
	// APITokenOIDCInsecureSkipVerifyTag disables TLS verification for the
	// identity provider only, separately from any flag the consuming service
	// keeps for the resource server.
	APITokenOIDCInsecureSkipVerifyTag = "oidc_insecure_skip_verify"
)

// APITokenAuthMode is how a stored API token yields a bearer.
type APITokenAuthMode string

const (
	// APITokenAuthModeStatic means the stored secret is the bearer.
	APITokenAuthModeStatic APITokenAuthMode = "static"
	// APITokenAuthModeClientCredentials mints with the client credentials grant.
	APITokenAuthModeClientCredentials APITokenAuthMode = APITokenAuthMode(oidctoken.GrantClientCredentials)
	// APITokenAuthModePassword mints with the resource owner password grant.
	APITokenAuthModePassword APITokenAuthMode = APITokenAuthMode(oidctoken.GrantPassword)
	// APITokenAuthModeRefreshToken mints with the refresh token grant.
	APITokenAuthModeRefreshToken APITokenAuthMode = APITokenAuthMode(oidctoken.GrantRefreshToken)
)

// APITokenOIDCSecretVersion is the schema version of APITokenOIDCSecret.
const APITokenOIDCSecretVersion = 1

// APITokenOIDCSecret is the sealed secret set of an OIDC-mode token. All the
// secrets a mode needs travel together, so replacing one replaces the set.
type APITokenOIDCSecret struct {
	Version      int    `json:"version"`
	ClientSecret string `json:"client_secret,omitempty"`
	Password     string `json:"password,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

// Encode serializes the secret set for the Token field.
func (s APITokenOIDCSecret) Encode() (string, error) {
	if s.Version == 0 {
		s.Version = APITokenOIDCSecretVersion
	}
	encoded, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("encode OIDC secret: %w", err)
	}
	return string(encoded), nil
}

// DecodeAPITokenOIDCSecret parses the Token field of an OIDC-mode token.
func DecodeAPITokenOIDCSecret(raw string) (APITokenOIDCSecret, error) {
	secret := APITokenOIDCSecret{}
	if err := json.Unmarshal([]byte(raw), &secret); err != nil {
		return APITokenOIDCSecret{}, errors.New("stored OIDC credential secret is not valid")
	}
	if secret.Version != APITokenOIDCSecretVersion {
		return APITokenOIDCSecret{}, fmt.Errorf("stored OIDC credential version %d is not supported", secret.Version)
	}
	return secret, nil
}

// Flag reads a boolean opt-in from the free-form tags.
func (m APITokenMetadata) Flag(key string) bool {
	if m.Tags == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(m.Tags[key])) {
	case "true", "1", "yes":
		return true
	}
	return false
}

// AuthMode reports how the token yields a bearer. Tokens written before the
// mode existed have no tag and are static.
func (t *APIToken) AuthMode() APITokenAuthMode {
	mode := APITokenAuthMode(strings.TrimSpace(t.Metadata.Tags[APITokenAuthModeTag]))
	if mode == "" {
		return APITokenAuthModeStatic
	}
	return mode
}

// OIDCSource assembles the token source of an OIDC-mode token from its
// metadata and sealed secret. It fails for a static token.
func (t *APIToken) OIDCSource() (oidctoken.Source, APITokenOIDCSecret, error) {
	mode := t.AuthMode()
	if mode == APITokenAuthModeStatic {
		return oidctoken.Source{}, APITokenOIDCSecret{}, errors.New("api token is a static bearer, not an OIDC credential")
	}
	secret, err := DecodeAPITokenOIDCSecret(t.Token)
	if err != nil {
		return oidctoken.Source{}, APITokenOIDCSecret{}, err
	}
	tags := t.Metadata.Tags
	source := oidctoken.Source{
		Grant:              oidctoken.Grant(mode),
		Issuer:             strings.TrimSpace(tags[APITokenOIDCIssuerTag]),
		ClientID:           strings.TrimSpace(t.ID),
		ClientSecret:       secret.ClientSecret,
		Username:           strings.TrimSpace(tags[APITokenOIDCUsernameTag]),
		Password:           secret.Password,
		RefreshToken:       secret.RefreshToken,
		Scopes:             strings.TrimSpace(tags[APITokenOIDCScopesTag]),
		TokenField:         oidctoken.TokenField(strings.TrimSpace(tags[APITokenOIDCTokenFieldTag])),
		ClientAuthMethod:   oidctoken.ClientAuthMethod(strings.TrimSpace(tags[APITokenOIDCClientAuthMethodTag])),
		InsecureSkipVerify: t.Metadata.Flag(APITokenOIDCInsecureSkipVerifyTag),
	}
	if err := source.Validate(); err != nil {
		return oidctoken.Source{}, APITokenOIDCSecret{}, err
	}
	return source, secret, nil
}

// APITokenResolver turns a stored API token into the bearer a backend sends
// to the resource server. A static token is returned as stored; an OIDC
// token is minted through a shared oidctoken.Manager, so identical inputs
// across credentials share one cached token, and a refresh token the
// provider rotates is sealed back into the store before it is needed again.
//
// The host process that owns the admin store constructs one and hands it,
// or an interface it satisfies, to the services that make outbound calls.
type APITokenResolver struct {
	store  *Store
	tokens *oidctoken.Manager
}

// NewAPITokenResolver returns a resolver over the store with a fresh cache.
func NewAPITokenResolver(store *Store) *APITokenResolver {
	return &APITokenResolver{store: store, tokens: oidctoken.NewManager()}
}

// Bearer returns the bearer for a token that was read from the store (so its
// secret is open). accountID is the tenant the token belongs to, needed to
// persist a rotated refresh token.
func (r *APITokenResolver) Bearer(ctx context.Context, accountID string, token *APIToken) (string, error) {
	if token == nil {
		return "", errors.New("api token is nil")
	}
	if token.AuthMode() == APITokenAuthModeStatic {
		return token.Token, nil
	}
	if r == nil || r.tokens == nil {
		return "", errors.New("OIDC token manager is not configured")
	}
	source, secret, err := token.OIDCSource()
	if err != nil {
		return "", err
	}
	minted, err := r.tokens.Token(ctx, source)
	if err != nil {
		return "", err
	}
	if minted.RefreshToken != "" && minted.RefreshToken != secret.RefreshToken {
		if r.store == nil {
			return "", errors.New("admin store is not available to persist the rotated refresh token")
		}
		secret.RefreshToken = minted.RefreshToken
		encoded, err := secret.Encode()
		if err != nil {
			return "", fmt.Errorf("encode rotated refresh token: %w", err)
		}
		token.Token = encoded
		token.UpdatedAt = time.Now().UTC()
		if err := r.store.PutAPIToken(accountID, token); err != nil {
			return "", fmt.Errorf("store rotated refresh token: %w", err)
		}
	}
	return minted.Value, nil
}

// Forget drops the cached bearer of an OIDC token so the next Bearer call
// mints again, for use when the resource server rejects a token that has
// not expired (a revoked session, a rotated client secret). It is a no-op
// for a static token.
func (r *APITokenResolver) Forget(token *APIToken) {
	if r == nil || r.tokens == nil || token == nil || token.AuthMode() == APITokenAuthModeStatic {
		return
	}
	source, _, err := token.OIDCSource()
	if err != nil {
		return
	}
	r.tokens.Forget(source)
}
