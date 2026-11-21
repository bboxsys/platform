package admin

import (
	"testing"
	"time"

	"github.com/bboxsys/platform/user"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// rolesFromGroups
// ---------------------------------------------------------------------------

func TestRolesFromGroups(t *testing.T) {
	conn := &user.OIDCConnection{
		GroupRoleMappings: map[string]string{
			"admins":  user.RoleAdministrator,
			"staff":   user.RoleUser,
			"readers": user.RoleViewer,
			"bogus":   "superhero", // invalid target role, must be ignored
			"dupe":    user.RoleUser,
		},
		DefaultRoles: []string{user.RoleUser},
	}

	t.Run("maps groups to roles", func(t *testing.T) {
		got := rolesFromGroups(conn, []string{"admins", "readers"})
		assert.ElementsMatch(t, []string{user.RoleAdministrator, user.RoleViewer}, got)
	})

	t.Run("dedups repeated role targets", func(t *testing.T) {
		got := rolesFromGroups(conn, []string{"staff", "dupe"})
		assert.Equal(t, []string{user.RoleUser}, got)
	})

	t.Run("ignores unknown groups and invalid role targets", func(t *testing.T) {
		got := rolesFromGroups(conn, []string{"nope", "bogus"})
		// No valid mapping -> falls back to default roles.
		assert.Equal(t, []string{user.RoleUser}, got)
	})

	t.Run("falls back to default roles when nothing maps", func(t *testing.T) {
		got := rolesFromGroups(conn, []string{})
		assert.Equal(t, []string{user.RoleUser}, got)
	})

	t.Run("default roles are also restricted to valid roles", func(t *testing.T) {
		badConn := &user.OIDCConnection{
			GroupRoleMappings: map[string]string{},
			DefaultRoles:      []string{"nonsense", user.RoleViewer},
		}
		got := rolesFromGroups(badConn, nil)
		assert.Equal(t, []string{user.RoleViewer}, got)
	})
}

// ---------------------------------------------------------------------------
// OIDCConnection.Validate
// ---------------------------------------------------------------------------

func TestOIDCConnectionValidate(t *testing.T) {
	base := func() *user.OIDCConnection {
		return user.NewOIDCConnection("acct1", "corp", "https://idp.example.com", "client-id", "secret", "https://app.example.com/callback")
	}

	t.Run("valid connection passes", func(t *testing.T) {
		require.NoError(t, base().Validate())
	})

	t.Run("rejects non-https issuer", func(t *testing.T) {
		c := base()
		c.IssuerURL = "http://idp.example.com"
		assert.Error(t, c.Validate())
	})

	t.Run("allows http localhost issuer for dev", func(t *testing.T) {
		c := base()
		c.IssuerURL = "http://localhost:5556/dex"
		c.RedirectURI = "http://localhost:8080/callback"
		assert.NoError(t, c.Validate())
	})

	t.Run("rejects slug with separators", func(t *testing.T) {
		c := base()
		c.Slug = "bad/slug"
		assert.Error(t, c.Validate())
	})

	t.Run("rejects unknown mapped role", func(t *testing.T) {
		c := base()
		c.GroupRoleMappings = map[string]string{"g": "wizard"}
		assert.Error(t, c.Validate())
	})

	t.Run("rejects unknown default role", func(t *testing.T) {
		c := base()
		c.DefaultRoles = []string{"wizard"}
		assert.Error(t, c.Validate())
	})
}

func TestOIDCConnectionPublicOmitsSecret(t *testing.T) {
	c := user.NewOIDCConnection("acct1", "corp", "https://idp.example.com", "client-id", "top-secret", "https://app.example.com/callback")
	pub := c.Public()
	assert.Equal(t, "client-id", pub.ClientID)
	assert.Equal(t, "corp", pub.Slug)
	// PublicOIDCConnection has no ClientSecret field at all; assert the source
	// secret is not exposed through the projection's known fields.
	assert.NotContains(t, []string{pub.ClientID, pub.IssuerURL, pub.RedirectURI, pub.Slug}, "top-secret")
}

// ---------------------------------------------------------------------------
// OIDCConnection store CRUD + slug uniqueness + delete guard
// ---------------------------------------------------------------------------

func TestOIDCConnectionStoreCRUD(t *testing.T) {
	st := newTestStore(t)
	_, account := bootstrapAccount(t, st, "tenant-a")

	conn := user.NewOIDCConnection(account.ID, "acme", "https://idp.acme.com", "cid", "csecret", "https://app/callback")
	require.NoError(t, st.PutOIDCConnection(account.ID, conn))

	// By account
	got, err := st.GetOIDCConnectionByAccount(account.ID)
	require.NoError(t, err)
	assert.Equal(t, "acme", got.Slug)
	assert.Equal(t, "csecret", got.ClientSecret)

	// By slug
	bySlug, err := st.GetOIDCConnectionBySlug("acme")
	require.NoError(t, err)
	assert.Equal(t, account.ID, bySlug.AccountID)

	// Delete removes both the connection and the slug index.
	require.NoError(t, st.DeleteOIDCConnection(account.ID))
	_, err = st.GetOIDCConnectionByAccount(account.ID)
	assert.Error(t, err)
	_, err = st.GetOIDCConnectionBySlug("acme")
	assert.Error(t, err)
}

func TestOIDCConnectionSlugUniqueness(t *testing.T) {
	st := newTestStore(t)
	operator, accountA := bootstrapAccount(t, st, "tenant-a")
	accountB, err := st.NewAccount(user.AccountConfig{Name: "tenant-b", OperatorID: operator.ID})
	require.NoError(t, err)

	connA := user.NewOIDCConnection(accountA.ID, "shared", "https://idp.a.com", "cid", "sec", "https://a/callback")
	require.NoError(t, st.PutOIDCConnection(accountA.ID, connA))

	// Account B cannot claim the same slug.
	connB := user.NewOIDCConnection(accountB.ID, "shared", "https://idp.b.com", "cid", "sec", "https://b/callback")
	err = st.PutOIDCConnection(accountB.ID, connB)
	require.Error(t, err)

	// Slug still resolves to account A.
	resolved, err := st.GetOIDCConnectionBySlug("shared")
	require.NoError(t, err)
	assert.Equal(t, accountA.ID, resolved.AccountID)
}

func TestOIDCConnectionSlugChangeCleansOldIndex(t *testing.T) {
	st := newTestStore(t)
	_, account := bootstrapAccount(t, st, "tenant-a")

	conn := user.NewOIDCConnection(account.ID, "old", "https://idp.com", "cid", "sec", "https://app/callback")
	require.NoError(t, st.PutOIDCConnection(account.ID, conn))

	conn.Slug = "new"
	require.NoError(t, st.PutOIDCConnection(account.ID, conn))

	// Old slug no longer resolves; new one does.
	_, err := st.GetOIDCConnectionBySlug("old")
	assert.Error(t, err)
	byNew, err := st.GetOIDCConnectionBySlug("new")
	require.NoError(t, err)
	assert.Equal(t, account.ID, byNew.AccountID)

	// The freed slug can be claimed by another account.
	operator, _ := st.GetOperatorByName("operator")
	accountB, err := st.NewAccount(user.AccountConfig{Name: "tenant-b", OperatorID: operator.ID})
	require.NoError(t, err)
	connB := user.NewOIDCConnection(accountB.ID, "old", "https://idp.b.com", "cid", "sec", "https://b/callback")
	require.NoError(t, st.PutOIDCConnection(accountB.ID, connB))
}

func TestOIDCConnectionUnknownSlug(t *testing.T) {
	st := newTestStore(t)
	bootstrapAccount(t, st, "tenant-a")
	_, err := st.GetOIDCConnectionBySlug("does-not-exist")
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// State + nonce: generate / persist / single-use / expiry
// ---------------------------------------------------------------------------

func TestOIDCStateSingleUseAndExpiry(t *testing.T) {
	st := newTestStore(t)

	s1, err := randomToken()
	require.NoError(t, err)
	n1, err := randomToken()
	require.NoError(t, err)
	assert.NotEqual(t, s1, n1)

	record := user.NewOIDCState(s1, "acct-1", n1, "/dashboard", "invite-xyz")
	require.NoError(t, st.PutOAuthState(record))

	// Retrieve: nonce/account/invite/return path persisted.
	got, err := st.GetOAuthState(s1)
	require.NoError(t, err)
	assert.Equal(t, n1, got.Nonce)
	assert.Equal(t, "acct-1", got.AccountID)
	assert.Equal(t, "invite-xyz", got.InviteToken)
	assert.Equal(t, "/dashboard", got.RedirectURI)

	// Single-use: after delete, a replay lookup fails.
	require.NoError(t, st.DeleteOAuthState(s1))
	_, err = st.GetOAuthState(s1)
	assert.Error(t, err)

	// Expiry check.
	fresh := user.NewOIDCState("s2", "acct-1", "n2", "/", "")
	assert.False(t, fresh.IsExpired(10*time.Minute))
	stale := &user.OAuthState{State: "s3", CreatedAt: time.Now().Add(-20 * time.Minute)}
	assert.True(t, stale.IsExpired(10*time.Minute))
}

func TestRandomTokenUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		tok, err := randomToken()
		require.NoError(t, err)
		require.NotEmpty(t, tok)
		require.False(t, seen[tok], "randomToken produced a duplicate")
		seen[tok] = true
	}
}

// ---------------------------------------------------------------------------
// Legacy Google identity backward-compat resolution + lazy migration
// ---------------------------------------------------------------------------

func TestResolveFederatedIdentityLegacyGoogle(t *testing.T) {
	st := newTestStore(t)
	_, account := bootstrapAccount(t, st, "tenant-a")

	// Simulate an identity created by the deprecated bespoke Google flow, stored
	// under the legacy "google" provider key.
	legacy := user.NewOAuthIdentity(user.OAuthProviderGoogle, "sub-123", "u@example.com", "U Example", "", "acct:u@example.com", true)
	require.NoError(t, st.PutOAuthIdentity(account.ID, legacy))

	// New issuer-based lookup must find it via the legacy fallback.
	identity, resolvedAccount, foundKey, err := st.ResolveFederatedIdentity(user.GoogleIssuerURL, "sub-123")
	require.NoError(t, err)
	require.NotNil(t, identity)
	assert.Equal(t, account.ID, resolvedAccount)
	assert.Equal(t, user.LegacyGoogleProvider, foundKey)
	assert.Equal(t, "acct:u@example.com", identity.UserID)

	// A non-Google issuer must NOT fall back to the legacy google key.
	_, _, _, err = st.ResolveFederatedIdentity("https://idp.other.com", "sub-123")
	assert.Error(t, err)
}

func TestResolveFederatedIdentityPrefersIssuerKey(t *testing.T) {
	st := newTestStore(t)
	_, account := bootstrapAccount(t, st, "tenant-a")

	providerKey := user.ProviderKeyForIssuer(user.GoogleIssuerURL)
	fed := user.NewFederatedIdentity(providerKey, "sub-9", "n@example.com", "N", "", "acct:n@example.com", true)
	require.NoError(t, st.PutOAuthIdentity(account.ID, fed))

	identity, resolvedAccount, foundKey, err := st.ResolveFederatedIdentity(user.GoogleIssuerURL, "sub-9")
	require.NoError(t, err)
	assert.Equal(t, account.ID, resolvedAccount)
	assert.Equal(t, providerKey, foundKey)
	assert.Equal(t, "acct:n@example.com", identity.UserID)
}

func TestProviderKeyForIssuer(t *testing.T) {
	assert.Equal(t, "oidc:https://accounts.google.com", user.ProviderKeyForIssuer(user.GoogleIssuerURL))
	assert.Equal(t, "oidc:https://idp.example.com", user.ProviderKeyForIssuer("https://idp.example.com"))
}
