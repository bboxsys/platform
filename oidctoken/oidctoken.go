// Package oidctoken mints short-lived bearer tokens from durable OAuth 2.0 and
// OpenID Connect client inputs.
//
// A backend that calls a third-party API on a user's behalf usually holds a
// long-lived credential (a client secret, an automation user's password, or a
// refresh token) and needs a short-lived access token for each call. This
// package turns the former into the latter with standard facilities only:
//
//  1. read the issuer's /.well-known/openid-configuration document,
//  2. check that the advertised issuer matches the configured one,
//  3. use the advertised token endpoint and client authentication methods,
//  4. request a token with one of the standard grants, and
//  5. derive the expiry from expires_in or, failing that, the JWT exp claim.
//
// It is provider-neutral: nothing here knows about a particular identity
// provider or a particular resource server. Manager adds a per-source cache
// that renews shortly before expiry and collapses concurrent mints.
package oidctoken

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Grant is an OAuth 2.0 grant type this package can request.
type Grant string

const (
	// GrantClientCredentials authenticates the client itself: a service or
	// machine-to-machine identity with a client secret.
	GrantClientCredentials Grant = "client_credentials"
	// GrantPassword is the resource owner password grant. It is a legacy flow
	// kept for providers that still require it; prefer the other two.
	GrantPassword Grant = "password"
	// GrantRefreshToken renews an access token from a refresh token obtained
	// through an interactive flow elsewhere.
	GrantRefreshToken Grant = "refresh_token"
)

// TokenField selects which token from the response is used as the bearer.
type TokenField string

const (
	// TokenFieldAccess sends the access token. This is the default and what
	// almost every resource server expects.
	TokenFieldAccess TokenField = "access_token"
	// TokenFieldID sends the ID token, for a resource server configured to
	// accept one.
	TokenFieldID TokenField = "id_token"
)

// ClientAuthMethod is how the client authenticates to the token endpoint.
type ClientAuthMethod string

const (
	// ClientAuthAuto chooses from the methods discovery advertises: basic
	// when supported, else post, and none when there is no client secret.
	ClientAuthAuto ClientAuthMethod = "auto"
	// ClientAuthSecretBasic sends the client ID and secret as HTTP basic auth.
	ClientAuthSecretBasic ClientAuthMethod = "client_secret_basic"
	// ClientAuthSecretPost sends the client ID and secret in the form body.
	ClientAuthSecretPost ClientAuthMethod = "client_secret_post"
	// ClientAuthNone sends only the client ID, for a public client.
	ClientAuthNone ClientAuthMethod = "none"
)

const (
	responseLimit  = 1 << 20
	requestTimeout = 20 * time.Second
)

// Source is the durable input a token is minted from. Everything except the
// secrets is safe to persist as metadata; ClientSecret, Password, and
// RefreshToken must be stored sealed.
type Source struct {
	Grant        Grant
	Issuer       string
	ClientID     string
	ClientSecret string
	Username     string
	Password     string
	RefreshToken string
	// Scopes is the space-separated scope parameter, sent as given.
	Scopes string
	// TokenField defaults to TokenFieldAccess when empty.
	TokenField TokenField
	// ClientAuthMethod defaults to ClientAuthAuto when empty.
	ClientAuthMethod ClientAuthMethod
	// InsecureSkipVerify disables TLS verification for discovery and the
	// token endpoint only. It says nothing about the resource server the
	// minted token is sent to.
	InsecureSkipVerify bool
}

// Token is a minted bearer. RefreshToken is set when the provider returned
// one, which for the refresh grant may be a rotated replacement the caller
// must persist. A zero ExpiresAt means the provider gave no expiry and the
// token cannot be cached.
type Token struct {
	Value        string
	RefreshToken string
	ExpiresAt    time.Time
}

// Validate reports whether the source has what its grant needs.
func (s Source) Validate() error {
	switch s.Grant {
	case GrantClientCredentials:
		if s.ClientSecret == "" {
			return errors.New("OIDC client credentials require a client secret")
		}
	case GrantPassword:
		if s.Username == "" || s.Password == "" {
			return errors.New("OIDC password grant requires a username and password")
		}
	case GrantRefreshToken:
		if s.RefreshToken == "" {
			return errors.New("OIDC refresh grant requires a refresh token")
		}
	default:
		return fmt.Errorf("OIDC grant %q is not supported", s.Grant)
	}
	if s.Issuer == "" || s.ClientID == "" {
		return errors.New("OIDC issuer and client ID are required")
	}
	switch s.TokenField {
	case "", TokenFieldAccess, TokenFieldID:
	default:
		return fmt.Errorf("OIDC token field %q is not supported", s.TokenField)
	}
	switch s.ClientAuthMethod {
	case "", ClientAuthAuto, ClientAuthSecretBasic, ClientAuthSecretPost, ClientAuthNone:
	default:
		return fmt.Errorf("OIDC client authentication method %q is not supported", s.ClientAuthMethod)
	}
	return nil
}

type discovery struct {
	Issuer                   string   `json:"issuer"`
	TokenEndpoint            string   `json:"token_endpoint"`
	GrantTypesSupported      []string `json:"grant_types_supported"`
	TokenEndpointAuthMethods []string `json:"token_endpoint_auth_methods_supported"`
}

type tokenResponse struct {
	AccessToken  string          `json:"access_token"`
	IDToken      string          `json:"id_token"`
	RefreshToken string          `json:"refresh_token"`
	ExpiresIn    json.RawMessage `json:"expires_in"`
	Error        string          `json:"error"`
}

// Mint requests one token from the source's issuer. It performs discovery on
// every call; use Manager to cache.
func Mint(ctx context.Context, source Source) (Token, error) {
	if err := source.Validate(); err != nil {
		return Token{}, err
	}
	issuer, err := secureURL(source.Issuer, "issuer")
	if err != nil {
		return Token{}, err
	}
	discoveryURL := strings.TrimSuffix(issuer.String(), "/") + "/.well-known/openid-configuration"
	client := httpClient(source.InsecureSkipVerify)
	doc := discovery{}
	if err := jsonRequest(ctx, client, http.MethodGet, discoveryURL, nil, nil, &doc); err != nil {
		return Token{}, fmt.Errorf("OIDC discovery: %w", err)
	}
	if normalizeIssuer(doc.Issuer) != normalizeIssuer(source.Issuer) {
		return Token{}, errors.New("OIDC discovery issuer does not match the configured issuer")
	}
	tokenEndpoint, err := secureURL(doc.TokenEndpoint, "token endpoint")
	if err != nil {
		return Token{}, err
	}
	if len(doc.GrantTypesSupported) > 0 && !contains(doc.GrantTypesSupported, string(source.Grant)) {
		return Token{}, fmt.Errorf("OIDC provider does not advertise the %s grant", source.Grant)
	}

	form := url.Values{"grant_type": {string(source.Grant)}}
	if source.Scopes != "" {
		form.Set("scope", source.Scopes)
	}
	switch source.Grant {
	case GrantPassword:
		form.Set("username", source.Username)
		form.Set("password", source.Password)
	case GrantRefreshToken:
		form.Set("refresh_token", source.RefreshToken)
	}
	authMethod, err := clientAuthMethod(source, doc.TokenEndpointAuthMethods)
	if err != nil {
		return Token{}, err
	}
	headers := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}
	switch authMethod {
	case ClientAuthSecretBasic:
		credentials := url.QueryEscape(source.ClientID) + ":" + url.QueryEscape(source.ClientSecret)
		headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credentials)))
	case ClientAuthSecretPost:
		form.Set("client_id", source.ClientID)
		form.Set("client_secret", source.ClientSecret)
	default:
		form.Set("client_id", source.ClientID)
	}
	response := tokenResponse{}
	if err := jsonRequest(ctx, client, http.MethodPost, tokenEndpoint.String(), strings.NewReader(form.Encode()), headers, &response); err != nil {
		return Token{}, fmt.Errorf("OIDC token mint: %w", err)
	}
	if response.Error != "" {
		return Token{}, fmt.Errorf("OIDC token endpoint returned %s", response.Error)
	}
	field := source.TokenField
	if field == "" {
		field = TokenFieldAccess
	}
	value := response.AccessToken
	if field == TokenFieldID {
		value = response.IDToken
	}
	if value == "" {
		return Token{}, fmt.Errorf("OIDC token endpoint returned no %s", field)
	}
	return Token{
		Value:        value,
		RefreshToken: response.RefreshToken,
		ExpiresAt:    Expiration(value, response.ExpiresIn, time.Now()),
	}, nil
}

// secureURL parses an issuer or endpoint and insists on https with a host and
// no embedded credentials. Query and fragment are dropped.
func secureURL(raw, label string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("OIDC %s is not a URL: %w", label, err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return nil, fmt.Errorf("OIDC %s must be an HTTPS URL with a host", label)
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed, nil
}

func normalizeIssuer(raw string) string {
	return strings.TrimSuffix(strings.TrimSpace(raw), "/")
}

func httpClient(insecureSkipVerify bool) *http.Client {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		transport = &http.Transport{}
	} else {
		transport = transport.Clone()
	}
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: insecureSkipVerify} //nolint:gosec // per-source opt-in
	return &http.Client{
		Transport: transport,
		Timeout:   requestTimeout,
		// A redirect from the issuer would move credentials to another host.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func jsonRequest(
	ctx context.Context, client *http.Client, method, endpoint string, body io.Reader, headers http.Header, out any,
) error {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(response.Body, responseLimit))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		failure := tokenResponse{}
		_ = decoder.Decode(&failure)
		if failure.Error != "" {
			return fmt.Errorf("HTTP %d (%s)", response.StatusCode, failure.Error)
		}
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// clientAuthMethod resolves the configured method against what the provider
// advertises. An empty advertisement means the provider did not say, and the
// configured or default method is used as is.
func clientAuthMethod(source Source, supported []string) (ClientAuthMethod, error) {
	method := source.ClientAuthMethod
	if method == "" || method == ClientAuthAuto {
		switch {
		case source.ClientSecret == "":
			method = ClientAuthNone
		case contains(supported, string(ClientAuthSecretBasic)) || len(supported) == 0:
			method = ClientAuthSecretBasic
		case contains(supported, string(ClientAuthSecretPost)):
			method = ClientAuthSecretPost
		default:
			return "", errors.New("OIDC provider advertises no supported client-secret authentication method")
		}
	}
	if len(supported) > 0 && method != ClientAuthNone && !contains(supported, string(method)) {
		return "", fmt.Errorf("OIDC provider does not advertise %s", method)
	}
	if method != ClientAuthNone && source.ClientSecret == "" {
		return "", fmt.Errorf("OIDC %s requires a client secret", method)
	}
	return method, nil
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// Expiration derives when a token expires from the response's expires_in
// (a number or a numeric string) and, when that is absent, from the exp
// claim of a JWT. It returns the zero time when neither is available.
func Expiration(token string, expiresIn json.RawMessage, now time.Time) time.Time {
	if seconds, ok := parseExpiresIn(expiresIn); ok && seconds > 0 {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	claims := struct {
		ExpiresAt int64 `json:"exp"`
	}{}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.ExpiresAt <= 0 {
		return time.Time{}
	}
	return time.Unix(claims.ExpiresAt, 0)
}

func parseExpiresIn(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		seconds, err := strconv.ParseInt(number.String(), 10, 64)
		return seconds, err == nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, false
	}
	seconds, err := strconv.ParseInt(text, 10, 64)
	return seconds, err == nil
}
