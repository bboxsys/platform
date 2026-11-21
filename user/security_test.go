package user

import (
	"strings"
	"testing"

	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestUser mints an account keypair and a user signed by it.
func newTestUser(t *testing.T, roles []string) (nkeys.KeyPair, string, *User) {
	t.Helper()
	kp, err := nkeys.CreateAccount()
	require.NoError(t, err)
	pk, err := kp.PublicKey()
	require.NoError(t, err)
	u, _, err := NewUser(&UserConfig{
		Email:     "expiry@test.local",
		Password:  "pw",
		Roles:     roles,
		AccountID: pk,
	}, kp)
	require.NoError(t, err)
	return kp, pk, u
}

// TestValidateUserTokenExpiry locks in the security fix that ValidateUserToken
// rejects an expired token instead of trusting it indefinitely.
func TestValidateUserTokenExpiry(t *testing.T) {
	kp, pk, u := newTestUser(t, []string{RoleUser})

	t.Run("expired token rejected", func(t *testing.T) {
		// Negative minutesValid makes the token already expired at mint time.
		expired, err := NewUserToken(u, -10, kp)
		require.NoError(t, err)
		claims, err := ValidateUserToken(expired.String(), pk)
		require.Error(t, err)
		assert.Nil(t, claims)
		assert.Contains(t, strings.ToLower(err.Error()), "expired")
	})

	t.Run("zero minutes rejected", func(t *testing.T) {
		zero, err := NewUserToken(u, 0, kp)
		require.NoError(t, err)
		_, err = ValidateUserToken(zero.String(), pk)
		require.Error(t, err)
		assert.Contains(t, strings.ToLower(err.Error()), "expired")
	})

	t.Run("valid token accepted", func(t *testing.T) {
		valid, err := NewUserToken(u, 60, kp)
		require.NoError(t, err)
		claims, err := ValidateUserToken(valid.String(), pk)
		require.NoError(t, err)
		require.NotNil(t, claims)
		assert.Equal(t, u.PublicKey, claims.Subject)
	})

	t.Run("wrong issuer rejected", func(t *testing.T) {
		valid, err := NewUserToken(u, 60, kp)
		require.NoError(t, err)
		otherKP, err := nkeys.CreateAccount()
		require.NoError(t, err)
		otherPK, err := otherKP.PublicKey()
		require.NoError(t, err)
		_, err = ValidateUserToken(valid.String(), otherPK)
		require.Error(t, err)
	})
}

func TestInvitationLifecycle(t *testing.T) {
	inv, err := NewInvitation(&InvitationConfig{
		Email: "invitee@test.local",
		Roles: []string{RoleUser},
	}, "acct-123", "inviter-1")
	require.NoError(t, err)
	require.NotNil(t, inv)

	// Freshly created invitations are pending, not expired, and acceptable.
	assert.Equal(t, InvitationPending, inv.Status)
	assert.NotEmpty(t, inv.Token)
	assert.NotEmpty(t, inv.ID)
	assert.Equal(t, "acct-123", inv.AccountID)
	assert.True(t, inv.IsPending())
	assert.False(t, inv.IsExpired())
	assert.True(t, inv.CanBeAccepted())

	t.Run("default ttl applied", func(t *testing.T) {
		// TTLHours <= 0 defaults to 72 hours in the future.
		assert.True(t, inv.ExpiresAt.After(inv.CreatedAt))
	})

	t.Run("expired invitation cannot be accepted", func(t *testing.T) {
		expired, err := NewInvitation(&InvitationConfig{Email: "e@test.local", TTLHours: 1}, "acct-123", "inviter-1")
		require.NoError(t, err)
		// Force expiry.
		expired.ExpiresAt = expired.CreatedAt.Add(-1)
		assert.True(t, expired.IsExpired())
		assert.False(t, expired.CanBeAccepted())
	})

	t.Run("accept marks accepted", func(t *testing.T) {
		inv.Accept("user-abc")
		assert.Equal(t, InvitationAccepted, inv.Status)
		assert.Equal(t, "user-abc", inv.AcceptedBy)
		require.NotNil(t, inv.AcceptedAt)
		assert.False(t, inv.IsPending())
		assert.False(t, inv.CanBeAccepted())
	})

	t.Run("revoke marks revoked", func(t *testing.T) {
		other, err := NewInvitation(&InvitationConfig{Email: "r@test.local"}, "acct-123", "inviter-1")
		require.NoError(t, err)
		other.Revoke()
		assert.Equal(t, InvitationRevoked, other.Status)
		assert.False(t, other.IsPending())
		assert.False(t, other.CanBeAccepted())
	})
}

func TestRoles(t *testing.T) {
	assert.True(t, IsValidRole(RoleAdministrator))
	assert.True(t, IsValidRole(RoleUser))
	assert.True(t, IsValidRole(RoleViewer))
	assert.False(t, IsValidRole("administrators")) // legacy plural is not canonical
	assert.False(t, IsValidRole("bogus"))

	// HasRole matches canonical names.
	assert.True(t, HasRole([]string{RoleUser, RoleAdministrator}, RoleAdministrator))
	assert.False(t, HasRole([]string{RoleUser}, RoleAdministrator))

	// HasRole also accepts legacy plural forms mapping to the canonical role.
	assert.True(t, HasRole([]string{"administrators"}, RoleAdministrator))
	assert.True(t, HasRole([]string{"viewers"}, RoleViewer))
	assert.False(t, HasRole([]string{"viewers"}, RoleAdministrator))
}

func TestUserClaimsEncodeDecode(t *testing.T) {
	kp, pk, u := newTestUser(t, []string{RoleAdministrator})

	claims := NewUserClaims(u)
	assert.Equal(t, u.PublicKey, claims.Subject)
	assert.Equal(t, u.GetID(), claims.User)
	assert.Equal(t, []string{RoleAdministrator}, claims.Roles)

	// ExpectedPrefixes must permit account-issued JWTs.
	prefixes := claims.ExpectedPrefixes()
	require.NotEmpty(t, prefixes)
	assert.Equal(t, nkeys.PrefixByteAccount, prefixes[0])

	encoded, err := claims.Encode(kp)
	require.NoError(t, err)
	require.NotEmpty(t, encoded)

	decoded, err := DecodeUserClaims(encoded)
	require.NoError(t, err)
	assert.Equal(t, u.PublicKey, decoded.Subject)
	assert.Equal(t, pk, decoded.Issuer)
}
