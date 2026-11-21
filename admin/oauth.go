package admin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/bboxsys/platform/user"
)

var (
	ErrOAuthNotEnabled     = errors.New("oauth is not enabled")
	ErrOAuthInvalidState   = errors.New("invalid oauth state")
	ErrOAuthStateExpired   = errors.New("oauth state has expired")
	ErrOAuthCodeExchange   = errors.New("failed to exchange authorization code")
	ErrOAuthUserInfo       = errors.New("failed to get user info from provider")
	ErrOAuthInvalidIDToken = errors.New("invalid id token")
)

// OAuthConfig contains OAuth configuration
type OAuthConfig struct {
	Enabled                  bool          `json:"enabled"`
	GoogleClientID           string        `json:"google_client_id"`
	GoogleClientSecret       string        `json:"google_client_secret"`
	RedirectURI              string        `json:"redirect_uri"`
	StateTTL                 time.Duration `json:"state_ttl"`
	AllowSelfServiceAccounts bool          `json:"allow_self_service_accounts"`
}

// GoogleUserInfo contains user information from Google's userinfo endpoint
type GoogleUserInfo struct {
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	GivenName     string `json:"given_name"`
	FamilyName    string `json:"family_name"`
	Picture       string `json:"picture"`
}

// GoogleOAuth handles Google OAuth operations.
//
// Deprecated: Google is now modeled as a preconfigured OIDC connection and login
// runs through the unified OIDC flow (see admin/oidc.go and the /oauth/google/*
// shims in server.go). This type is retained only so GetOAuthConfig can report
// the public Google client config to the current admin UI. Its ExchangeCode path
// is unsound (it never verifies the id_token) and must not be used for login.
type GoogleOAuth struct {
	config      *oauth2.Config
	oauthConfig *OAuthConfig
	store       *Store
}

// NewGoogleOAuth creates a new Google OAuth handler
func NewGoogleOAuth(config *OAuthConfig, store *Store) *GoogleOAuth {
	oauth2Config := &oauth2.Config{
		ClientID:     config.GoogleClientID,
		ClientSecret: config.GoogleClientSecret,
		RedirectURL:  config.RedirectURI,
		Scopes: []string{
			"openid",
			"email",
			"profile",
		},
		Endpoint: google.Endpoint,
	}

	return &GoogleOAuth{
		config:      oauth2Config,
		oauthConfig: config,
		store:       store,
	}
}

// GetAuthorizationURL generates an authorization URL for Google OAuth
func (g *GoogleOAuth) GetAuthorizationURL(inviteToken string) (string, string, error) {
	if !g.oauthConfig.Enabled {
		return "", "", ErrOAuthNotEnabled
	}

	// Generate a random state parameter
	state, err := generateRandomState()
	if err != nil {
		return "", "", err
	}

	// Store the state for later verification
	oauthState := user.NewOAuthState(state, g.oauthConfig.RedirectURI, inviteToken)
	if err := g.store.PutOAuthState(oauthState); err != nil {
		return "", "", err
	}

	// Generate the authorization URL
	url := g.config.AuthCodeURL(state, oauth2.AccessTypeOffline)

	return url, state, nil
}

// ExchangeCode exchanges an authorization code for tokens and user info
func (g *GoogleOAuth) ExchangeCode(ctx context.Context, code, state string) (*GoogleUserInfo, *user.OAuthState, error) {
	if !g.oauthConfig.Enabled {
		return nil, nil, ErrOAuthNotEnabled
	}

	// Verify the state parameter
	oauthState, err := g.store.GetOAuthState(state)
	if err != nil {
		return nil, nil, ErrOAuthInvalidState
	}

	// Check if state has expired
	if oauthState.IsExpired(g.oauthConfig.StateTTL) {
		// Clean up expired state
		g.store.DeleteOAuthState(state)
		return nil, nil, ErrOAuthStateExpired
	}

	// Delete the state to prevent reuse
	g.store.DeleteOAuthState(state)

	// Exchange the code for tokens
	token, err := g.config.Exchange(ctx, code)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrOAuthCodeExchange, err)
	}

	// Get user info from Google
	userInfo, err := g.getUserInfo(ctx, token)
	if err != nil {
		return nil, nil, err
	}

	return userInfo, oauthState, nil
}

// getUserInfo fetches user information from Google's userinfo endpoint
func (g *GoogleOAuth) getUserInfo(ctx context.Context, token *oauth2.Token) (*GoogleUserInfo, error) {
	client := g.config.Client(ctx, token)

	resp, err := client.Get("https://openidconnect.googleapis.com/v1/userinfo")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOAuthUserInfo, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%w: status %d: %s", ErrOAuthUserInfo, resp.StatusCode, string(body))
	}

	var userInfo GoogleUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOAuthUserInfo, err)
	}

	return &userInfo, nil
}

// GetPublicConfig returns the public OAuth configuration (no secrets)
func (g *GoogleOAuth) GetPublicConfig() map[string]interface{} {
	return map[string]interface{}{
		"enabled": g.oauthConfig.Enabled,
		"google": map[string]string{
			"client_id":    g.oauthConfig.GoogleClientID,
			"redirect_uri": g.oauthConfig.RedirectURI,
		},
		"allow_self_service_accounts": g.oauthConfig.AllowSelfServiceAccounts,
	}
}

// generateRandomState generates a cryptographically secure random state string
func generateRandomState() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(bytes), nil
}
