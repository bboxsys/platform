package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bboxsys/platform/user"
	"github.com/bboxsys/store"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecret = "hs-bearer-3f9c1c2e-do-not-leak"

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

func TestAPITokenNormalizeDefaultsToUserScope(t *testing.T) {
	tok := APIToken{Name: "legacy"}
	tok.Normalize()
	assert.Equal(t, APITokenScopeUser, tok.Scope)

	legacy := APIToken{Name: "legacy", Token: "x"}
	view := legacy.View()
	assert.Equal(t, APITokenScopeUser, view.Scope)
	assert.True(t, view.HasToken)
}

func TestAPITokenKey(t *testing.T) {
	const owner = "UABCDEF"
	key, err := apiTokenKey(APITokenScopeUser, owner, "ci")
	require.NoError(t, err)
	assert.Equal(t, owner+":ci", key, "user scope must be byte-identical to the pre-scope key")

	key, err = apiTokenKey(APITokenScopeAccount, owner, "ci")
	require.NoError(t, err)
	assert.Equal(t, APITokenAccountOwner+":ci", key)

	_, err = apiTokenKey(APITokenScopeUser, APITokenAccountOwner, "ci")
	assert.ErrorIs(t, err, ErrReservedAPITokenUser, "a user whose id is the reserved owner must not reach the shared namespace")
	_, err = apiTokenKey(APITokenScopeUser, "", "ci")
	assert.ErrorIs(t, err, ErrReservedAPITokenUser)
}

func TestValidateAPITokenName(t *testing.T) {
	for _, name := range []string{"ci", "thunderforest-outdoors", "hs prod", "a.b_c"} {
		assert.True(t, ValidateAPITokenName(name), name)
	}
	for _, name := range []string{"", " ci", "ci ", "a:b", "a/b", "a\\b", "a?b", "a#b", strings.Repeat("x", 129)} {
		assert.False(t, ValidateAPITokenName(name), name)
	}
}

func TestAPITokenViewHasNoSecretField(t *testing.T) {
	tok := APIToken{Name: "n", Type: APITokenTypeBearer, Token: testSecret, UserID: "U1"}
	body, err := json.Marshal(tok.View())
	require.NoError(t, err)
	assert.NotContains(t, string(body), testSecret)
	assert.NotContains(t, string(body), `"token"`)
	assert.Contains(t, string(body), `"has_token":true`)
}

// ---------------------------------------------------------------------------
// Secrets
// ---------------------------------------------------------------------------

func TestSecretBoxRoundTrip(t *testing.T) {
	key := make([]byte, secretsKeyLength)
	for i := range key {
		key[i] = byte(i)
	}
	box, err := newSecretBox(key)
	require.NoError(t, err)

	sealed, err := box.seal(testSecret)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(sealed, sealedPrefix))
	assert.NotContains(t, sealed, testSecret)

	plain, err := box.open(sealed)
	require.NoError(t, err)
	assert.Equal(t, testSecret, plain)

	// Legacy plaintext and empty values pass through.
	plain, err = box.open("plain-legacy")
	require.NoError(t, err)
	assert.Equal(t, "plain-legacy", plain)
	sealed, err = box.seal("")
	require.NoError(t, err)
	assert.Equal(t, "", sealed)

	// A different key cannot open it.
	key[0] ^= 0xff
	other, err := newSecretBox(key)
	require.NoError(t, err)
	_, err = other.open(mustSeal(t, box, testSecret))
	assert.Error(t, err)

	_, err = newSecretBox([]byte("short"))
	assert.ErrorIs(t, err, errSecretsKeyLength)
}

func mustSeal(t *testing.T, box *secretBox, v string) string {
	t.Helper()
	s, err := box.seal(v)
	require.NoError(t, err)
	return s
}

func TestSecretsKeyFileCreatedBesideStore(t *testing.T) {
	root := t.TempDir()
	location := filepath.Join(root, "admin.db")
	st, err := NewStore(&AdminStoreOptions{Location: location, Application: "unit_testing", Component: "admin_store"})
	require.NoError(t, err)
	require.NoError(t, st.Close())

	keyPath := filepath.Join(root, secretsKeyFileName)
	info, err := os.Stat(keyPath)
	require.NoError(t, err, "secrets.key must be created next to the store directory")
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	first, err := loadOrCreateSecretsKey(keyPath)
	require.NoError(t, err)
	second, err := loadOrCreateSecretsKey(keyPath)
	require.NoError(t, err)
	assert.Equal(t, first, second, "an existing key file must be reused, not regenerated")
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

func TestStoreAPITokenSealedAtRest(t *testing.T) {
	st := newTestStore(t)
	_, account := bootstrapAccount(t, st, "acct")
	tok := NewAPIToken(APIToken{Name: "ci", Type: APITokenTypeBearer, Token: testSecret}, "UOWNER")
	require.NoError(t, st.PutAPIToken(account.ID, tok))
	assert.Equal(t, testSecret, tok.Token, "PutAPIToken must not mutate the caller's copy")

	raw, err := st.GetOneRaw(store.ScopedKey(account.ID, "apitoken", "UOWNER:ci"))
	require.NoError(t, err)
	assert.NotContains(t, raw, testSecret, "the raw record must not hold the plaintext secret")
	assert.Contains(t, raw, sealedPrefix)

	got, err := st.GetAPIToken(account.ID, "UOWNER", "ci")
	require.NoError(t, err)
	assert.Equal(t, testSecret, got.Token)
	assert.Equal(t, APITokenScopeUser, got.Scope)
}

func TestStoreAPITokenLegacyPlaintextRecord(t *testing.T) {
	st := newTestStore(t)
	_, account := bootstrapAccount(t, st, "acct")
	// A record written before scope and sealing existed: no scope key, plain
	// secret, keyed {userID}:{name}.
	legacy := map[string]any{"name": "old", "type": "custom", "id": "", "token": testSecret, "user_id": "UOWNER"}
	require.NoError(t, st.PutScoped(account.ID, "apitoken", "UOWNER:old", legacy))

	got, err := st.GetAPIToken(account.ID, "UOWNER", "old")
	require.NoError(t, err)
	assert.Equal(t, testSecret, got.Token)
	assert.Equal(t, APITokenScopeUser, got.Scope, "a record without scope must normalize to user")

	own, err := st.GetAPITokensByUser(account.ID, "UOWNER")
	require.NoError(t, err)
	require.Len(t, own, 1)
	assert.Equal(t, APITokenScopeUser, own[0].Scope)

	// Re-writing it seals the secret.
	require.NoError(t, st.PutAPIToken(account.ID, got))
	raw, err := st.GetOneRaw(store.ScopedKey(account.ID, "apitoken", "UOWNER:old"))
	require.NoError(t, err)
	assert.NotContains(t, raw, testSecret)
}

func TestStoreAPITokenVisibility(t *testing.T) {
	st := newTestStore(t)
	_, account := bootstrapAccount(t, st, "acct")
	const alice, bob = "UALICE", "UBOB"
	require.NoError(t, st.PutAPIToken(account.ID, NewAPIToken(APIToken{Name: "mine", Type: APITokenTypeCustom, Token: "a"}, alice)))
	require.NoError(t, st.PutAPIToken(account.ID, NewAPIToken(APIToken{Name: "mine", Type: APITokenTypeCustom, Token: "b"}, bob)))
	shared := NewAPIToken(APIToken{
		Name: "controller", Type: APITokenTypeBearer, Scope: APITokenScopeAccount, Token: "c",
		Metadata: APITokenMetadata{ResourceType: "example_deployment", ResourceID: "dep-1", Endpoint: "https://api.example"},
	}, alice)
	require.NoError(t, st.PutAPIToken(account.ID, shared))

	names := func(tokens []APIToken) []string {
		out := make([]string, 0, len(tokens))
		for _, tk := range tokens {
			out = append(out, string(tk.Scope)+":"+tk.Name+":"+tk.Token)
		}
		return out
	}

	aliceSees, err := st.GetAPITokensVisibleTo(account.ID, alice)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"user:mine:a", "account:controller:c"}, names(aliceSees))

	bobSees, err := st.GetAPITokensVisibleTo(account.ID, bob)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"user:mine:b", "account:controller:c"}, names(bobSees))

	// ByUser excludes the account token even for its creator.
	aliceOwn, err := st.GetAPITokensByUser(account.ID, alice)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"user:mine:a"}, names(aliceOwn))

	acct, err := st.GetAccountAPITokens(account.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"account:controller:c"}, names(acct))

	// Bob cannot read Alice's user-scoped token by name, but can read the shared one.
	_, err = st.GetAPIToken(account.ID, bob, "controller")
	assert.Error(t, err)
	got, err := st.GetAPITokenVisibleTo(account.ID, bob, "controller")
	require.NoError(t, err)
	assert.Equal(t, "c", got.Token)
	got, err = st.GetAPITokenVisibleTo(account.ID, bob, "mine")
	require.NoError(t, err)
	assert.Equal(t, "b", got.Token, "a user-scoped token wins over an account one of the same name")

	byRes, err := st.GetAPITokensByResource(account.ID, bob, "example_deployment", "dep-1")
	require.NoError(t, err)
	require.Len(t, byRes, 1)
	assert.Equal(t, "https://api.example", byRes[0].Metadata.Endpoint)

	byType, err := st.GetAPITokensVisibleToByType(account.ID, bob, APITokenTypeBearer)
	require.NoError(t, err)
	assert.Len(t, byType, 1)

	// Deleting by scope removes only that record.
	require.NoError(t, st.DeleteAccountAPIToken(account.ID, "controller"))
	_, err = st.GetAccountAPIToken(account.ID, "controller")
	assert.Error(t, err)
	require.NoError(t, st.DeleteAPIToken(account.ID, alice, "mine"))
	_, err = st.GetAPIToken(account.ID, bob, "mine")
	assert.NoError(t, err)

	// The reserved owner can never write into the shared namespace.
	err = st.PutAPIToken(account.ID, NewAPIToken(APIToken{Name: "x", Type: APITokenTypeCustom}, APITokenAccountOwner))
	assert.ErrorIs(t, err, ErrReservedAPITokenUser)
	// A bad name is rejected before it becomes a key.
	err = st.PutAPIToken(account.ID, NewAPIToken(APIToken{Name: "a:b", Type: APITokenTypeCustom}, alice))
	assert.ErrorIs(t, err, ErrInvalidAPITokenName)
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// tokenTestUser is a user in a tenant account plus a bearer token for them.
type tokenTestUser struct {
	user   *user.User
	bearer string
}

func makeTokenTestUser(t *testing.T, adminServer *AdminServer, accountID, email string, roles []string) tokenTestUser {
	t.Helper()
	u, err := adminServer.Store().NewUser(&user.UserConfig{Email: email, Password: "pw", Roles: roles, AccountID: accountID})
	require.NoError(t, err)
	kp, err := adminServer.Store().GetAccountKeyPair(accountID)
	require.NoError(t, err)
	tok, err := user.NewUserToken(u, 60, kp)
	require.NoError(t, err)
	return tokenTestUser{user: u, bearer: tok.String()}
}

// tokenRequest builds a request as the given user, through the ID middleware.
func tokenRequest(t *testing.T, accountID string, u tokenTestUser, method, target string, body string, vars map[string]string) *http.Request {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	r.Header.Set("X-Blackbox-AccountID", accountID)
	r.Header.Set("authorization", "Bearer "+u.bearer)
	if vars != nil {
		r = mux.SetURLVars(r, vars)
	}
	return r
}

func TestAPITokenHandlers(t *testing.T) {
	adminServer := newTestAdminServer(t, t.TempDir()+"/admin.db")
	adminAccountID := adminServer.AdminAccountID()
	tenant := makeAccount(t, adminServer, "tenant")
	admin := makeTokenTestUser(t, adminServer, tenant, "admin@tenant.local", []string{user.RoleAdministrator})
	member := makeTokenTestUser(t, adminServer, tenant, "member@tenant.local", []string{user.RoleUser})

	// Every route, wrapped exactly as AddHandlers wraps it. The test server
	// has not run AddHandlers, so build the same Auth it would.
	auth := NewAuth(AuthConfig{
		Store:           adminServer.Store(),
		Logger:          adminServer.Logger,
		BasicAuth:       true,
		TrustedAccounts: []string{adminAccountID, tenant},
	})
	bearer := auth.Bearer
	requireAdmin := func(h http.HandlerFunc) http.HandlerFunc { return auth.RequireAdmin(adminAccountID, h) }
	do := func(u tokenTestUser, h http.HandlerFunc, method, target, body string, vars map[string]string) *httptest.ResponseRecorder {
		return serveWithID(adminServer, h, tokenRequest(t, tenant, u, method, target, body, vars))
	}
	nameVar := func(n string) map[string]string { return map[string]string{"name": n} }

	t.Run("member creates a user token and cannot read the secret back", func(t *testing.T) {
		body := `{"name":"mine","type":"custom","id":"me@x","token":"` + testSecret + `"}`
		rr := do(member, bearer(adminServer.CreateAPIToken), http.MethodPost, "/api/v1/apitokens", body, nil)
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		var view APITokenView
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &view))
		assert.Equal(t, APITokenScopeUser, view.Scope)
		assert.True(t, view.HasToken)
		assert.Equal(t, member.user.PublicKey, view.UserID)

		rr = do(member, bearer(adminServer.CreateAPIToken), http.MethodPost, "/api/v1/apitokens", body, nil)
		assert.Equal(t, http.StatusConflict, rr.Code)
		rr = do(member, bearer(adminServer.CreateAPIToken), http.MethodPost, "/api/v1/apitokens", `{"name":"x"}`, nil)
		assert.Equal(t, http.StatusBadRequest, rr.Code, "type stays required")
		rr = do(member, bearer(adminServer.CreateAPIToken), http.MethodPost, "/api/v1/apitokens", `{"name":"a/b","type":"custom"}`, nil)
		assert.Equal(t, http.StatusBadRequest, rr.Code, "name must be a safe path and key segment")
	})

	t.Run("account token is administrator only to write, visible to all", func(t *testing.T) {
		body := `{"name":"controller","type":"bearer","token":"` + testSecret + `","metadata":{"resource_type":"example_deployment","resource_id":"dep-1","endpoint":"https://api.example"}}`
		rr := do(member, requireAdmin(adminServer.CreateAccountAPIToken), http.MethodPost, "/api/v1/accounts/me/apitokens", body, nil)
		assert.Equal(t, http.StatusForbidden, rr.Code)

		rr = do(admin, requireAdmin(adminServer.CreateAccountAPIToken), http.MethodPost, "/api/v1/accounts/me/apitokens", body, nil)
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		var view APITokenView
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &view))
		assert.Equal(t, APITokenScopeAccount, view.Scope)
		assert.Equal(t, "dep-1", view.Metadata.ResourceID)

		// A client cannot smuggle a scope through the user route.
		rr = do(member, bearer(adminServer.CreateAPIToken), http.MethodPost, "/api/v1/apitokens", `{"name":"sneaky","type":"custom","scope":"account","token":"s"}`, nil)
		require.Equal(t, http.StatusCreated, rr.Code)
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &view))
		assert.Equal(t, APITokenScopeUser, view.Scope)

		rr = do(member, bearer(adminServer.GetAccountAPIToken), http.MethodGet, "/api/v1/accounts/me/apitokens/controller", "", nameVar("controller"))
		assert.Equal(t, http.StatusOK, rr.Code)
		rr = do(member, bearer(adminServer.GetAccountAPITokens), http.MethodGet, "/api/v1/accounts/me/apitokens", "", nil)
		assert.Equal(t, http.StatusOK, rr.Code)
		var list []APITokenView
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
		require.Len(t, list, 1)

		rr = do(member, requireAdmin(adminServer.UpdateAccountAPIToken), http.MethodPut, "/api/v1/accounts/me/apitokens/controller", `{"id":"x"}`, nameVar("controller"))
		assert.Equal(t, http.StatusForbidden, rr.Code)
		rr = do(member, requireAdmin(adminServer.DeleteAccountAPIToken), http.MethodDelete, "/api/v1/accounts/me/apitokens/controller", "", nameVar("controller"))
		assert.Equal(t, http.StatusForbidden, rr.Code)
	})

	t.Run("list shows own user tokens plus account tokens", func(t *testing.T) {
		rr := do(member, bearer(adminServer.GetAPITokens), http.MethodGet, "/api/v1/apitokens", "", nil)
		require.Equal(t, http.StatusOK, rr.Code)
		var list []APITokenView
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
		got := map[string]APITokenScope{}
		for _, v := range list {
			got[v.Name] = v.Scope
		}
		assert.Equal(t, map[string]APITokenScope{"mine": APITokenScopeUser, "sneaky": APITokenScopeUser, "controller": APITokenScopeAccount}, got)

		rr = do(admin, bearer(adminServer.GetAPITokens), http.MethodGet, "/api/v1/apitokens", "", nil)
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
		require.Len(t, list, 1, "the admin sees the account token but not the member's user tokens")
		assert.Equal(t, "controller", list[0].Name)

		// The user route does not resolve account tokens.
		rr = do(member, bearer(adminServer.GetAPIToken), http.MethodGet, "/api/v1/apitokens/controller", "", nameVar("controller"))
		assert.Equal(t, http.StatusNotFound, rr.Code)
	})

	t.Run("update preserves the secret when absent or masked", func(t *testing.T) {
		for _, body := range []string{`{"id":"renamed"}`, `{"token":"` + APITokenMaskedSecret + `"}`, `{"token":""}`} {
			rr := do(member, bearer(adminServer.UpdateAPIToken), http.MethodPut, "/api/v1/apitokens/mine", body, nameVar("mine"))
			require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
			stored, err := adminServer.Store().GetAPIToken(tenant, member.user.PublicKey, "mine")
			require.NoError(t, err)
			assert.Equal(t, testSecret, stored.Token, body)
		}
		rr := do(admin, requireAdmin(adminServer.UpdateAccountAPIToken), http.MethodPut, "/api/v1/accounts/me/apitokens/controller", `{"token":"rotated","metadata":{"resource_type":"example_deployment","resource_id":"dep-2"}}`, nameVar("controller"))
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		stored, err := adminServer.Store().GetAccountAPIToken(tenant, "controller")
		require.NoError(t, err)
		assert.Equal(t, "rotated", stored.Token)
		assert.Equal(t, "dep-2", stored.Metadata.ResourceID)
		// Restore so the residency table below still has the known secret.
		stored.Token = testSecret
		require.NoError(t, adminServer.Store().PutAPIToken(tenant, stored))

		rr = do(member, bearer(adminServer.UpdateAPIToken), http.MethodPut, "/api/v1/apitokens/nope", `{}`, nameVar("nope"))
		assert.Equal(t, http.StatusNotFound, rr.Code)
	})

	t.Run("residency: no response body on any route contains a stored secret", func(t *testing.T) {
		routes := []struct {
			name   string
			u      tokenTestUser
			h      http.HandlerFunc
			method string
			target string
			body   string
			vars   map[string]string
		}{
			{"list", member, bearer(adminServer.GetAPITokens), http.MethodGet, "/api/v1/apitokens", "", nil},
			{"get", member, bearer(adminServer.GetAPIToken), http.MethodGet, "/api/v1/apitokens/mine", "", nameVar("mine")},
			{"update", member, bearer(adminServer.UpdateAPIToken), http.MethodPut, "/api/v1/apitokens/mine", `{"id":"y"}`, nameVar("mine")},
			{"create", member, bearer(adminServer.CreateAPIToken), http.MethodPost, "/api/v1/apitokens", `{"name":"fresh","type":"custom","token":"` + testSecret + `"}`, nil},
			{"account list", member, bearer(adminServer.GetAccountAPITokens), http.MethodGet, "/api/v1/accounts/me/apitokens", "", nil},
			{"account get", member, bearer(adminServer.GetAccountAPIToken), http.MethodGet, "/api/v1/accounts/me/apitokens/controller", "", nameVar("controller")},
			{"account update", admin, requireAdmin(adminServer.UpdateAccountAPIToken), http.MethodPut, "/api/v1/accounts/me/apitokens/controller", `{"id":"z"}`, nameVar("controller")},
			{"account create", admin, requireAdmin(adminServer.CreateAccountAPIToken), http.MethodPost, "/api/v1/accounts/me/apitokens", `{"name":"fresh","type":"bearer","token":"` + testSecret + `"}`, nil},
			{"delete", member, bearer(adminServer.DeleteAPIToken), http.MethodDelete, "/api/v1/apitokens/fresh", "", nameVar("fresh")},
			{"account delete", admin, requireAdmin(adminServer.DeleteAccountAPIToken), http.MethodDelete, "/api/v1/accounts/me/apitokens/fresh", "", nameVar("fresh")},
		}
		for _, rt := range routes {
			rr := do(rt.u, rt.h, rt.method, rt.target, rt.body, rt.vars)
			assert.Less(t, rr.Code, 300, "%s: %s", rt.name, rr.Body.String())
			assert.NotContains(t, rr.Body.String(), testSecret, rt.name)
			assert.NotContains(t, rr.Body.String(), `"token"`, rt.name)
		}
	})

	t.Run("owner deletes their token", func(t *testing.T) {
		rr := do(admin, bearer(adminServer.DeleteAPIToken), http.MethodDelete, "/api/v1/apitokens/mine", "", nameVar("mine"))
		assert.Equal(t, http.StatusNotFound, rr.Code, "another user's token is not visible to delete")
		rr = do(member, bearer(adminServer.DeleteAPIToken), http.MethodDelete, "/api/v1/apitokens/mine", "", nameVar("mine"))
		assert.Equal(t, http.StatusNoContent, rr.Code)
		_, err := adminServer.Store().GetAPIToken(tenant, member.user.PublicKey, "mine")
		assert.Error(t, err)
	})
}
