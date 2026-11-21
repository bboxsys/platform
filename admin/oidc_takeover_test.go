package admin

import (
	"testing"

	"github.com/bboxsys/platform/user"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSyncFederatedUserNoCrossTenantTakeover verifies the takeover gates in
// syncFederatedUser: a federated login can never link to a local user in a
// DIFFERENT account, and linking to a pre-existing local account requires the
// IdP to have verified the email.
func TestSyncFederatedUserNoCrossTenantTakeover(t *testing.T) {
	adminServer := newTestAdminServer(t, t.TempDir()+"/admin.db")
	accountA := makeAccount(t, adminServer, "tenant-a")
	accountB := makeAccount(t, adminServer, "tenant-b")

	// A local admin user exists in account B.
	victimEmail := "admin@tenant-b.example"
	_, err := adminServer.Store().NewUser(&user.UserConfig{
		Email:     victimEmail,
		AccountID: accountB,
		Password:  "s3cret-pass",
		Roles:     []string{user.RoleAdministrator},
	})
	require.NoError(t, err)

	// Account A configures its own OIDC connection.
	connA := &user.OIDCConnection{
		AccountID:     accountA,
		Slug:          "tenant-a",
		Enabled:       true,
		IssuerURL:     "https://idp.tenant-a.example",
		ClientID:      "client-a",
		AutoProvision: true,
		DefaultRoles:  []string{user.RoleUser},
	}

	t.Run("cannot link to a user in another account", func(t *testing.T) {
		// A's IdP asserts the victim's email (verified). Must NOT log into B.
		claims := &OIDCClaims{Subject: "sub-a-1", Email: victimEmail, EmailVerified: true}
		_, _, err := adminServer.syncFederatedUser(connA, claims, accountA, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "different account")
	})

	t.Run("cannot link to a same-account local user with an unverified email", func(t *testing.T) {
		localEmail := "local@tenant-a.example"
		_, err := adminServer.Store().NewUser(&user.UserConfig{
			Email:     localEmail,
			AccountID: accountA,
			Password:  "s3cret-pass",
			Roles:     []string{user.RoleUser},
		})
		require.NoError(t, err)

		claims := &OIDCClaims{Subject: "sub-a-2", Email: localEmail, EmailVerified: false}
		_, _, err = adminServer.syncFederatedUser(connA, claims, accountA, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not verified")
	})

	t.Run("links to a same-account local user with a verified email", func(t *testing.T) {
		linkEmail := "link@tenant-a.example"
		created, err := adminServer.Store().NewUser(&user.UserConfig{
			Email:     linkEmail,
			AccountID: accountA,
			Password:  "s3cret-pass",
			Roles:     []string{user.RoleUser},
		})
		require.NoError(t, err)

		claims := &OIDCClaims{Subject: "sub-a-3", Email: linkEmail, EmailVerified: true}
		u, isNew, err := adminServer.syncFederatedUser(connA, claims, accountA, nil)
		require.NoError(t, err)
		assert.False(t, isNew)
		assert.Equal(t, created.ID, u.ID)
	})

	t.Run("JIT-provisions a brand-new user into the connection account", func(t *testing.T) {
		claims := &OIDCClaims{Subject: "sub-a-4", Email: "new@tenant-a.example", EmailVerified: true}
		u, isNew, err := adminServer.syncFederatedUser(connA, claims, accountA, nil)
		require.NoError(t, err)
		assert.True(t, isNew)
		assert.Equal(t, accountA, u.AccountID)
	})
}

// TestSyncFederatedUserSameIssuerNoCrossLink verifies that two tenants using the
// same public issuer (e.g. both configuring Google) with the SAME subject do NOT
// cross-link: each resolves only its own account's identity, so the same IdP
// subject becomes a distinct user per tenant.
func TestSyncFederatedUserSameIssuerNoCrossLink(t *testing.T) {
	adminServer := newTestAdminServer(t, t.TempDir()+"/admin.db")
	accountA := makeAccount(t, adminServer, "tenant-a")
	accountB := makeAccount(t, adminServer, "tenant-b")

	sharedIssuer := user.GoogleIssuerURL
	subject := "shared-google-subject-123"

	connA := &user.OIDCConnection{AccountID: accountA, Slug: "a", Enabled: true, IssuerURL: sharedIssuer, ClientID: "client-a", AutoProvision: true, DefaultRoles: []string{user.RoleUser}}
	connB := &user.OIDCConnection{AccountID: accountB, Slug: "b", Enabled: true, IssuerURL: sharedIssuer, ClientID: "client-b", AutoProvision: true, DefaultRoles: []string{user.RoleUser}}

	// Same subject logs into each tenant with a tenant-specific email.
	uA, isNewA, err := adminServer.syncFederatedUser(connA, &OIDCClaims{Subject: subject, Email: "person@tenant-a.example", EmailVerified: true}, accountA, nil)
	require.NoError(t, err)
	assert.True(t, isNewA)
	assert.Equal(t, accountA, uA.AccountID)

	uB, isNewB, err := adminServer.syncFederatedUser(connB, &OIDCClaims{Subject: subject, Email: "person@tenant-b.example", EmailVerified: true}, accountB, nil)
	require.NoError(t, err)
	assert.True(t, isNewB, "same subject in a different tenant must create a distinct user, not resolve tenant A's identity")
	assert.Equal(t, accountB, uB.AccountID)
	assert.NotEqual(t, uA.ID, uB.ID)

	// A second login to tenant A with the same subject now resolves the existing
	// A identity (not a new user).
	uA2, isNewA2, err := adminServer.syncFederatedUser(connA, &OIDCClaims{Subject: subject, Email: "person@tenant-a.example", EmailVerified: true}, accountA, nil)
	require.NoError(t, err)
	assert.False(t, isNewA2)
	assert.Equal(t, uA.ID, uA2.ID)
}
