package admin

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bboxsys/platform/oidctoken/oidctokentest"
)

func newSecretsTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(&AdminStoreOptions{
		Location:    filepath.Join(t.TempDir(), "admin.db"),
		Application: "unit_testing",
		Component:   "apitoken_oidc",
		SecretsKey:  make([]byte, 32),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestAPITokenAuthModeDefaultsToStatic(t *testing.T) {
	token := &APIToken{Name: "legacy", Token: "bearer"}
	assert.Equal(t, APITokenAuthModeStatic, token.AuthMode())
	_, _, err := token.OIDCSource()
	assert.Error(t, err, "a static token has no OIDC source")

	resolver := NewAPITokenResolver(nil)
	bearer, err := resolver.Bearer(context.Background(), "account-1", token)
	require.NoError(t, err)
	assert.Equal(t, "bearer", bearer)
}

func TestAPITokenOIDCSecretRoundTrip(t *testing.T) {
	encoded, err := APITokenOIDCSecret{ClientSecret: "s", RefreshToken: "r"}.Encode()
	require.NoError(t, err)
	decoded, err := DecodeAPITokenOIDCSecret(encoded)
	require.NoError(t, err)
	assert.Equal(t, APITokenOIDCSecretVersion, decoded.Version)
	assert.Equal(t, "s", decoded.ClientSecret)
	assert.Equal(t, "r", decoded.RefreshToken)

	_, err = DecodeAPITokenOIDCSecret("not json")
	assert.Error(t, err)
	_, err = DecodeAPITokenOIDCSecret(`{"version":2}`)
	assert.Error(t, err)
}

func TestAPITokenMetadataFlag(t *testing.T) {
	meta := APITokenMetadata{Tags: map[string]string{"a": "true", "b": " Yes ", "c": "0"}}
	assert.True(t, meta.Flag("a"))
	assert.True(t, meta.Flag("b"))
	assert.False(t, meta.Flag("c"))
	assert.False(t, meta.Flag("missing"))
	assert.False(t, APITokenMetadata{}.Flag("a"))
}

func TestAPITokenOIDCSourceReadsTagsAndSecret(t *testing.T) {
	secret, err := APITokenOIDCSecret{ClientSecret: "client-secret", Password: "pw"}.Encode()
	require.NoError(t, err)
	token := &APIToken{
		Name:  "idp",
		ID:    " client-id ",
		Token: secret,
		Metadata: APITokenMetadata{Tags: map[string]string{
			APITokenAuthModeTag:               string(APITokenAuthModePassword),
			APITokenOIDCIssuerTag:             "https://idp.example.com/realm",
			APITokenOIDCUsernameTag:           "automation",
			APITokenOIDCScopesTag:             "openid offline_access",
			APITokenOIDCTokenFieldTag:         "id_token",
			APITokenOIDCClientAuthMethodTag:   "client_secret_post",
			APITokenOIDCInsecureSkipVerifyTag: "true",
		}},
	}
	source, got, err := token.OIDCSource()
	require.NoError(t, err)
	assert.Equal(t, "client-id", source.ClientID)
	assert.Equal(t, "https://idp.example.com/realm", source.Issuer)
	assert.Equal(t, "automation", source.Username)
	assert.Equal(t, "pw", source.Password)
	assert.Equal(t, "client-secret", source.ClientSecret)
	assert.Equal(t, "openid offline_access", source.Scopes)
	assert.EqualValues(t, "id_token", source.TokenField)
	assert.EqualValues(t, "client_secret_post", source.ClientAuthMethod)
	assert.True(t, source.InsecureSkipVerify)
	assert.Equal(t, "client-secret", got.ClientSecret)
}

func TestAPITokenResolverPersistsRotatedRefreshToken(t *testing.T) {
	issuer := oidctokentest.NewIssuer(t, "client_secret_basic")
	issuer.SetResponse("refresh_token", "rotated-refresh-token")
	store := newSecretsTestStore(t)

	secret, err := APITokenOIDCSecret{ClientSecret: "client-secret", RefreshToken: "original-refresh-token"}.Encode()
	require.NoError(t, err)
	token := NewAPIToken(APIToken{
		Name:  "renewable",
		Type:  APITokenTypeBearer,
		Scope: APITokenScopeUser,
		ID:    "client",
		Token: secret,
		Metadata: APITokenMetadata{
			ResourceType: "example_api",
			Endpoint:     "https://api.example.com",
			Tags: map[string]string{
				APITokenAuthModeTag:               string(APITokenAuthModeRefreshToken),
				APITokenOIDCIssuerTag:             issuer.URL(),
				APITokenOIDCScopesTag:             "openid offline_access",
				APITokenOIDCInsecureSkipVerifyTag: "true",
			},
		},
	}, "user-1")
	require.NoError(t, store.PutAPIToken("account-1", token))

	resolver := NewAPITokenResolver(store)
	visible, err := store.GetAPITokenVisibleTo("account-1", "user-1", token.Name)
	require.NoError(t, err)
	bearer, err := resolver.Bearer(context.Background(), "account-1", visible)
	require.NoError(t, err)
	assert.Equal(t, "minted-access-token", bearer)

	stored, err := store.GetAPIToken("account-1", "user-1", token.Name)
	require.NoError(t, err)
	rotated, err := DecodeAPITokenOIDCSecret(stored.Token)
	require.NoError(t, err)
	assert.Equal(t, "rotated-refresh-token", rotated.RefreshToken)
	assert.NotEqual(t, bearer, stored.Token, "the minted access token must not replace the renewable credential")

	// The next resolution reads the rotated credential and finds the token
	// that was just minted instead of minting again.
	again, err := resolver.Bearer(context.Background(), "account-1", stored)
	require.NoError(t, err)
	assert.Equal(t, bearer, again)
	assert.Equal(t, 1, issuer.TokenCalls())

	resolver.Forget(stored)
	_, err = resolver.Bearer(context.Background(), "account-1", stored)
	require.NoError(t, err)
	assert.Equal(t, 2, issuer.TokenCalls())
}
