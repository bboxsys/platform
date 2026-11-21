package oidctoken_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/bboxsys/platform/oidctoken"
	"github.com/bboxsys/platform/oidctoken/oidctokentest"
)

func TestClientCredentialsDiscoveryAndCache(t *testing.T) {
	issuer := oidctokentest.NewIssuer(t, "client_secret_basic", "client_secret_post")
	source := oidctoken.Source{
		Grant:              oidctoken.GrantClientCredentials,
		Issuer:             issuer.URL(),
		ClientID:           "service-client",
		ClientSecret:       "client-secret",
		Scopes:             "openid email profile",
		InsecureSkipVerify: true,
	}
	manager := oidctoken.NewManager()

	first, err := manager.Token(context.Background(), source)
	if err != nil {
		t.Fatalf("first token: %v", err)
	}
	second, err := manager.Token(context.Background(), source)
	if err != nil {
		t.Fatalf("second token: %v", err)
	}
	if first.Value != "minted-access-token" || second.Value != first.Value {
		t.Fatalf("tokens = %q, %q", first.Value, second.Value)
	}
	if issuer.TokenCalls() != 1 {
		t.Fatalf("token endpoint calls = %d, want 1", issuer.TokenCalls())
	}
	form := issuer.Form()
	if form.Get("grant_type") != "client_credentials" {
		t.Errorf("grant = %q", form.Get("grant_type"))
	}
	if user, pass := issuer.BasicAuth(); user != source.ClientID || pass != source.ClientSecret {
		t.Errorf("basic auth = %q/%q", user, pass)
	}
	if got := form.Get("client_id"); got != "" {
		t.Errorf("client_id was duplicated in a client_secret_basic request: %q", got)
	}
	if got := form.Get("scope"); got != source.Scopes {
		t.Errorf("scope = %q", got)
	}
}

func TestPasswordGrantWithPostAuthentication(t *testing.T) {
	issuer := oidctokentest.NewIssuer(t, "client_secret_post")
	source := oidctoken.Source{
		Grant:              oidctoken.GrantPassword,
		Issuer:             issuer.URL(),
		ClientID:           "legacy-client",
		ClientSecret:       "client-secret",
		Username:           "automation@example.com",
		Password:           "user-password",
		Scopes:             "openid email offline_access",
		ClientAuthMethod:   oidctoken.ClientAuthSecretPost,
		InsecureSkipVerify: true,
	}

	if _, err := oidctoken.Mint(context.Background(), source); err != nil {
		t.Fatalf("Mint: %v", err)
	}
	form := issuer.Form()
	for key, want := range map[string]string{
		"grant_type":    "password",
		"client_id":     source.ClientID,
		"client_secret": source.ClientSecret,
		"username":      source.Username,
		"password":      source.Password,
		"scope":         source.Scopes,
	} {
		if got := form.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestRefreshTokenRotationIsCachedUnderBothSources(t *testing.T) {
	issuer := oidctokentest.NewIssuer(t, "client_secret_basic")
	issuer.SetResponse("refresh_token", "rotated-refresh-token")
	source := oidctoken.Source{
		Grant:              oidctoken.GrantRefreshToken,
		Issuer:             issuer.URL(),
		ClientID:           "client",
		ClientSecret:       "secret",
		RefreshToken:       "original-refresh-token",
		InsecureSkipVerify: true,
	}
	manager := oidctoken.NewManager()
	first, err := manager.Token(context.Background(), source)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if first.RefreshToken != "rotated-refresh-token" {
		t.Fatalf("refresh token = %q", first.RefreshToken)
	}
	rotated := source
	rotated.RefreshToken = first.RefreshToken
	second, err := manager.Token(context.Background(), rotated)
	if err != nil {
		t.Fatalf("Token with rotated refresh token: %v", err)
	}
	if second.Value != first.Value || issuer.TokenCalls() != 1 {
		t.Errorf("rotated source minted again: calls = %d", issuer.TokenCalls())
	}
	manager.Forget(rotated)
	if _, err := manager.Token(context.Background(), rotated); err != nil {
		t.Fatalf("Token after Forget: %v", err)
	}
	if issuer.TokenCalls() != 2 {
		t.Errorf("calls after Forget = %d, want 2", issuer.TokenCalls())
	}
}

func TestRejectsDiscoveryIssuerMismatch(t *testing.T) {
	issuer := oidctokentest.NewIssuer(t, "client_secret_basic")
	source := oidctoken.Source{
		Grant:              oidctoken.GrantClientCredentials,
		Issuer:             issuer.URL() + "/unexpected",
		ClientID:           "client",
		ClientSecret:       "secret",
		InsecureSkipVerify: true,
	}
	if _, err := oidctoken.Mint(context.Background(), source); err == nil {
		t.Fatal("Mint accepted mismatched discovery")
	}
}

func TestRejectsPlainHTTPIssuer(t *testing.T) {
	source := oidctoken.Source{
		Grant:        oidctoken.GrantClientCredentials,
		Issuer:       "http://idp.example.com",
		ClientID:     "client",
		ClientSecret: "secret",
	}
	if _, err := oidctoken.Mint(context.Background(), source); err == nil {
		t.Fatal("Mint accepted an http issuer")
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]oidctoken.Source{
		"unknown grant":            {Grant: "implicit", Issuer: "https://idp", ClientID: "c"},
		"client credentials":       {Grant: oidctoken.GrantClientCredentials, Issuer: "https://idp", ClientID: "c"},
		"password without user":    {Grant: oidctoken.GrantPassword, Issuer: "https://idp", ClientID: "c", Password: "p"},
		"refresh without token":    {Grant: oidctoken.GrantRefreshToken, Issuer: "https://idp", ClientID: "c"},
		"missing issuer":           {Grant: oidctoken.GrantClientCredentials, ClientID: "c", ClientSecret: "s"},
		"unknown token field":      {Grant: oidctoken.GrantClientCredentials, Issuer: "https://idp", ClientID: "c", ClientSecret: "s", TokenField: "refresh_token"},
		"unknown client auth mode": {Grant: oidctoken.GrantClientCredentials, Issuer: "https://idp", ClientID: "c", ClientSecret: "s", ClientAuthMethod: "private_key_jwt"},
	}
	for name, source := range cases {
		if err := source.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, source)
		}
	}
	valid := oidctoken.Source{Grant: oidctoken.GrantClientCredentials, Issuer: "https://idp", ClientID: "c", ClientSecret: "s"}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid source rejected: %v", err)
	}
}

func TestExpirationSupportsStringAndJWT(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	if got := oidctoken.Expiration("opaque", json.RawMessage(`"120"`), now); !got.Equal(now.Add(120 * time.Second)) {
		t.Errorf("string expires_in = %v", got)
	}
	if got := oidctoken.Expiration("opaque", json.RawMessage(`90`), now); !got.Equal(now.Add(90 * time.Second)) {
		t.Errorf("numeric expires_in = %v", got)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"exp":1700000300}`))
	if got := oidctoken.Expiration(header+"."+payload+".", nil, now); !got.Equal(now.Add(300 * time.Second)) {
		t.Errorf("JWT expiration = %v", got)
	}
	if got := oidctoken.Expiration("opaque", nil, now); !got.IsZero() {
		t.Errorf("no expiry = %v, want zero", got)
	}
}
