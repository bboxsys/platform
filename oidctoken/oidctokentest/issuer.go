// Package oidctokentest provides a fake OpenID provider for tests of code
// that mints tokens with package oidctoken.
package oidctokentest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// Issuer is an in-process OpenID provider serving discovery and a token
// endpoint over TLS with a self-signed certificate, so sources pointed at it
// need InsecureSkipVerify. It records the last token request and counts the
// calls; Response is what the token endpoint returns and may be edited
// between requests.
type Issuer struct {
	Server *httptest.Server
	// Response is encoded as the token endpoint's JSON body.
	Response map[string]any

	mu         sync.Mutex
	tokenCalls int
	form       url.Values
	basicUser  string
	basicPass  string
}

// NewIssuer starts an issuer advertising the client_credentials, password,
// and refresh_token grants and the given token endpoint authentication
// methods. It is closed when the test ends.
func NewIssuer(t testing.TB, authMethods ...string) *Issuer {
	t.Helper()
	issuer := &Issuer{
		Response: map[string]any{
			"access_token": "minted-access-token",
			"expires_in":   3600,
			"token_type":   "Bearer",
		},
	}
	if authMethods == nil {
		authMethods = []string{}
	}
	issuer.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration"):
			writeJSON(t, w, map[string]any{
				"issuer":                                issuer.Server.URL,
				"token_endpoint":                        issuer.Server.URL + "/token",
				"grant_types_supported":                 []string{"client_credentials", "password", "refresh_token"},
				"token_endpoint_auth_methods_supported": authMethods,
			})
		case r.URL.Path == "/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("oidctokentest: ParseForm: %v", err)
			}
			user, pass, _ := r.BasicAuth()
			issuer.mu.Lock()
			issuer.tokenCalls++
			issuer.form = r.Form
			issuer.basicUser = user
			issuer.basicPass = pass
			response := issuer.Response
			issuer.mu.Unlock()
			writeJSON(t, w, response)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(issuer.Server.Close)
	return issuer
}

// URL is the issuer URL to configure in a Source.
func (i *Issuer) URL() string {
	return i.Server.URL
}

// TokenCalls is how many times the token endpoint has been called.
func (i *Issuer) TokenCalls() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.tokenCalls
}

// Form is the form the last token request carried.
func (i *Issuer) Form() url.Values {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.form
}

// BasicAuth is the HTTP basic credential the last token request carried.
func (i *Issuer) BasicAuth() (user, password string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.basicUser, i.basicPass
}

// SetResponse replaces one field of the token response.
func (i *Issuer) SetResponse(key string, value any) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.Response[key] = value
}

func writeJSON(t testing.TB, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("oidctokentest: encode response: %v", err)
	}
}
