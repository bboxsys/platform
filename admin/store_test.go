package admin

import (
	"testing"
	"time"

	"github.com/bboxsys/platform/user"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testUserConfigs = []user.UserConfig{
	{FirstName: "John", LastName: "Doe", Email: "jdoe@test.com", Password: "password123"},
	{FirstName: "Joe", LastName: "Buck", Email: "jbuck@test.com", Password: "password123"},
	{FirstName: "Troy", LastName: "Aikman", Email: "taikman@test.com", Password: "password123"},
	{FirstName: "Dak", LastName: "Prescott", Email: "tprescott@test.com", Password: "password123"},
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := NewStore(&AdminStoreOptions{
		Location:    t.TempDir(),
		Application: "unit_testing",
		Component:   "admin_store",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// bootstrapAccount creates an operator and one account under it.
func bootstrapAccount(t *testing.T, st *Store, name string) (user.Operator, user.Account) {
	t.Helper()
	operator, err := st.NewOperator(user.OperatorConfig{Name: "operator"})
	require.NoError(t, err)
	account, err := st.NewAccount(user.AccountConfig{Name: name, OperatorID: operator.ID})
	require.NoError(t, err)
	return operator, account
}

func TestStoreOperatorAndAccount(t *testing.T) {
	st := newTestStore(t)
	operator, account := bootstrapAccount(t, st, "acme")

	got, err := st.GetOperatorByID(operator.ID)
	require.NoError(t, err)
	assert.Equal(t, operator.ID, got.ID)

	byName, err := st.GetOperatorByName("operator")
	require.NoError(t, err)
	assert.Equal(t, operator.ID, byName.ID)

	_, err = st.GetOperatorKeyPair(operator.ID)
	require.NoError(t, err)

	acct, err := st.GetAccountByID(account.ID)
	require.NoError(t, err)
	assert.Equal(t, "acme", acct.Name)

	acctByName, err := st.GetAccountByName("acme")
	require.NoError(t, err)
	assert.Equal(t, account.ID, acctByName.ID)

	pub, err := st.GetAccountPublicKey(account.ID)
	require.NoError(t, err)
	assert.Equal(t, account.PublicKey, pub)

	_, err = st.GetAccountKeyPair(account.ID)
	require.NoError(t, err)

	accounts, err := st.GetAccounts()
	require.NoError(t, err)
	assert.Len(t, *accounts, 1)
}

func TestStoreUsersAndPasswords(t *testing.T) {
	st := newTestStore(t)
	_, account := bootstrapAccount(t, st, "acme")

	for _, cfg := range testUserConfigs {
		cfg.AccountID = account.ID
		cfg.Roles = []string{user.RoleUser}
		u, err := st.NewUser(&cfg)
		require.NoError(t, err, "creating %s", cfg.Email)
		assert.NotEmpty(t, u.ID)
		assert.NotEmpty(t, u.PublicKey)
	}

	users, err := st.GetUsers(account.ID)
	require.NoError(t, err)
	assert.Len(t, *users, len(testUserConfigs))

	// Email index resolves the user and its owning account.
	u, accountID, err := st.GetUserByEmail("taikman@test.com")
	require.NoError(t, err)
	assert.Equal(t, account.ID, accountID)
	assert.Equal(t, "taikman@test.com", u.Email)

	byID, err := st.GetUserByID(account.ID, u.ID)
	require.NoError(t, err)
	assert.Equal(t, u.ID, byID.ID)

	byKey, err := st.GetUserByPublicKey(account.ID, u.PublicKey)
	require.NoError(t, err)
	assert.Equal(t, u.ID, byKey.ID)

	valid, err := st.ValidateUserPassword(account.ID, u.ID, "password123")
	require.NoError(t, err)
	assert.True(t, valid, "correct password must validate")

	valid, err = st.ValidateUserPassword(account.ID, u.ID, "wrongpassword")
	require.NoError(t, err)
	assert.False(t, valid, "wrong password must not validate")

	require.NoError(t, st.DeleteUser(account.ID, u.ID))
	users, err = st.GetUsers(account.ID)
	require.NoError(t, err)
	assert.Len(t, *users, len(testUserConfigs)-1)
}

func TestStoreActivityLockoutAccounting(t *testing.T) {
	st := newTestStore(t)
	_, account := bootstrapAccount(t, st, "acme")

	activity := user.Activity{ID: "user-1", Active: true}
	require.NoError(t, st.PutActivity(account.ID, activity))

	// Failed logins accumulate via Update's additive merge.
	for i := 0; i < 2; i++ {
		require.NoError(t, st.UpdateActivity(account.ID, user.Activity{
			ID:               "user-1",
			FailedLoginCount: 1,
			LastFailedLogin:  time.Now(),
		}))
	}
	got, err := st.GetActivity(account.ID, "user-1")
	require.NoError(t, err)
	assert.Equal(t, 2, got.FailedLoginCount)
	assert.False(t, got.Locked)

	// Locking sticks once set.
	require.NoError(t, st.UpdateActivity(account.ID, user.Activity{
		ID:       "user-1",
		Locked:   true,
		LockedAt: time.Now(),
	}))
	got, err = st.GetActivity(account.ID, "user-1")
	require.NoError(t, err)
	assert.True(t, got.Locked)
	assert.False(t, got.LockedAt.IsZero())
}

func TestStoreServicesAndAgents(t *testing.T) {
	st := newTestStore(t)
	_, account := bootstrapAccount(t, st, "acme")

	svc, err := st.NewService(&user.UserConfig{Name: "media_server", AccountID: account.ID})
	require.NoError(t, err)
	assert.NotEmpty(t, svc.Token)
	assert.NotEmpty(t, svc.Seed)

	services, err := st.GetServices(account.ID)
	require.NoError(t, err)
	require.Len(t, *services, 1)
	assert.Equal(t, "media_server", (*services)[0].Name)

	byName, err := st.GetServiceByName(account.ID, "media_server")
	require.NoError(t, err)
	assert.Equal(t, svc.ID, byName.ID)

	require.NoError(t, st.DeleteService(account.ID, svc.ID))
	services, err = st.GetServices(account.ID)
	require.NoError(t, err)
	assert.Empty(t, *services)

	agent, err := st.NewAgent(&user.UserConfig{
		Name:      "agent",
		HostID:    "host-123",
		Hostname:  "pi-hub.local",
		AccountID: account.ID,
	})
	require.NoError(t, err)
	assert.Equal(t, "host-123", agent.HostID)

	agents, err := st.GetAgents(account.ID)
	require.NoError(t, err)
	assert.Len(t, *agents, 1)

	byHost, err := st.GetAgentByHostID(account.ID, "host-123")
	require.NoError(t, err)
	assert.Equal(t, "pi-hub.local", byHost.Hostname)

	require.NoError(t, st.DeleteAgent(account.ID, "host-123"))
	agents, err = st.GetAgents(account.ID)
	require.NoError(t, err)
	assert.Empty(t, *agents)
}

func TestStoreInvitations(t *testing.T) {
	st := newTestStore(t)
	_, account := bootstrapAccount(t, st, "acme")

	inv, err := user.NewInvitation(&user.InvitationConfig{
		Email:    "invitee@test.com",
		Roles:    []string{user.RoleUser},
		TTLHours: 1,
	}, account.ID, "tester@test.com")
	require.NoError(t, err)
	require.NoError(t, st.PutInvitation(inv))

	// Cross-account token index lookup.
	byToken, err := st.GetInvitationByToken(inv.Token)
	require.NoError(t, err)
	assert.Equal(t, "invitee@test.com", byToken.Email)
	assert.Equal(t, account.ID, byToken.AccountID)
	assert.Equal(t, user.InvitationPending, byToken.Status)

	byAccount, err := st.GetInvitationsByAccount(account.ID)
	require.NoError(t, err)
	assert.Len(t, byAccount, 1)

	inv.Status = user.InvitationRevoked
	require.NoError(t, st.UpdateInvitation(inv))
	byToken, err = st.GetInvitationByToken(inv.Token)
	require.NoError(t, err)
	assert.Equal(t, user.InvitationRevoked, byToken.Status)

	require.NoError(t, st.DeleteInvitation(inv.Token))
	_, err = st.GetInvitationByToken(inv.Token)
	assert.Error(t, err, "deleted invitation must not resolve")
}

func TestStoreServiceEndpoints(t *testing.T) {
	st := newTestStore(t)

	require.NoError(t, st.PutServiceEndpoint(NewServiceEndpoint("login_service", "https://login.app.com:8900")))
	require.NoError(t, st.PutServiceEndpoint(NewServiceEndpoint("proxy_service", "https://proxy.app.com:8901")))

	endpoints, err := st.GetServiceEndpoints()
	require.NoError(t, err)
	assert.Equal(t, "https://login.app.com:8900", endpoints["login_service"].URL)
	assert.Equal(t, "https://proxy.app.com:8901", endpoints["proxy_service"].URL)

	// Map put overwrites by kind.
	require.NoError(t, st.PutServiceEndpointsMap(map[string]ServiceEndpoint{
		"login_service": NewServiceEndpoint("login_service", "https://login2.app.com:8900"),
	}))
	endpoints, err = st.GetServiceEndpoints()
	require.NoError(t, err)
	assert.Equal(t, "https://login2.app.com:8900", endpoints["login_service"].URL)
}
