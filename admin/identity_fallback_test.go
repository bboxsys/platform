package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bboxsys/platform/user"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gate runs h behind the real ID middleware and Auth, the way AddHandlers
// wires them, with the tenant trusted, and returns the status for a call
// that presents token as its bearer in accountID.
func gate(t *testing.T, adminServer *AdminServer, accountID, token string, wrap func(*Auth, http.HandlerFunc) http.HandlerFunc) int {
	t.Helper()
	auth := NewAuth(AuthConfig{
		Store:           adminServer.st,
		Logger:          adminServer.Logger,
		TrustedAccounts: []string{adminServer.AdminAccountID(), accountID},
	})
	id := NewID(IDConfig{Store: adminServer.st, Logger: adminServer.Logger})
	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
	r := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	r.Header.Set("X-Blackbox-AccountID", accountID)
	r.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	id.Middleware(auth.Bearer(wrap(auth, ok))).ServeHTTP(rr, r)
	return rr.Code
}

func requireAdmin(adminServer *AdminServer) func(*Auth, http.HandlerFunc) http.HandlerFunc {
	return func(a *Auth, h http.HandlerFunc) http.HandlerFunc {
		return a.RequireAdmin(adminServer.AdminAccountID(), h)
	}
}

func requireSuperAdmin(adminServer *AdminServer) func(*Auth, http.HandlerFunc) http.HandlerFunc {
	return func(a *Auth, h http.HandlerFunc) http.HandlerFunc {
		return a.RequireSuperAdmin(adminServer.AdminAccountID(), h)
	}
}

func TestGetUserByPublicKeyFallsBackToAgentsAndServices(t *testing.T) {
	adminServer := newTestAdminServer(t, t.TempDir()+"/admin.db")
	tenant := makeAccount(t, adminServer, "tenant-identity")
	st := adminServer.Store()

	agent, err := st.NewAgent(&user.UserConfig{HostID: "host-1", Hostname: "host-1.local", AccountID: tenant, Roles: []string{user.RoleAdministrator}})
	require.NoError(t, err)
	svc, err := st.NewService(&user.UserConfig{Name: "media_service", AccountID: tenant, Roles: []string{user.RoleViewer}})
	require.NoError(t, err)

	u, err := st.GetUserByPublicKey(tenant, agent.PublicKey)
	require.NoError(t, err)
	assert.Equal(t, agent.PublicKey, u.PublicKey)
	assert.Equal(t, []string{user.RoleAdministrator}, u.Roles)
	assert.Equal(t, "host-1.local", u.FirstName)

	u, err = st.GetUserByPublicKey(tenant, svc.PublicKey)
	require.NoError(t, err)
	assert.Equal(t, svc.ID, u.ID)
	assert.Equal(t, []string{user.RoleViewer}, u.Roles)

	// Unknown keys behave as before: empty user, no error.
	u, err = st.GetUserByPublicKey(tenant, "UNKNOWN")
	require.NoError(t, err)
	assert.Empty(t, u.PublicKey)

	// Another account's agent is not visible.
	other := makeAccount(t, adminServer, "tenant-other")
	u, err = st.GetUserByPublicKey(other, agent.PublicKey)
	require.NoError(t, err)
	assert.Empty(t, u.PublicKey)

	// A user with the same key wins over the fallback (users are looked up first).
	usr, err := st.NewUser(&user.UserConfig{Email: "real@test.local", Password: "pw", AccountID: tenant, Roles: []string{user.RoleUser}})
	require.NoError(t, err)
	u, err = st.GetUserByPublicKey(tenant, usr.PublicKey)
	require.NoError(t, err)
	assert.Equal(t, usr.ID, u.ID)
}

func TestAgentAndServiceTokensPassRoleGates(t *testing.T) {
	adminServer := newTestAdminServer(t, t.TempDir()+"/admin.db")
	tenant := makeAccount(t, adminServer, "tenant-gates")
	st := adminServer.Store()

	adminAgent, err := st.NewAgent(&user.UserConfig{HostID: "host-admin", Hostname: "a", AccountID: tenant, Roles: []string{user.RoleAdministrator}})
	require.NoError(t, err)
	plainAgent, err := st.NewAgent(&user.UserConfig{HostID: "host-plain", Hostname: "b", AccountID: tenant})
	require.NoError(t, err)

	// Services created through ServiceCredentials have no roles.
	creds, err := adminServer.ServiceCredentials("tenant-gates", "worker")
	require.NoError(t, err)
	adminSvc, err := st.NewService(&user.UserConfig{Name: "admin_worker", AccountID: tenant, Roles: []string{user.RoleAdministrator}})
	require.NoError(t, err)

	cases := []struct {
		name  string
		token string
		admin int
	}{
		{"agent with administrator role", adminAgent.Token, http.StatusOK},
		{"agent without roles", plainAgent.Token, http.StatusForbidden},
		{"service with administrator role", adminSvc.Token, http.StatusOK},
		{"service from ServiceCredentials", creds.JWT, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.admin, gate(t, adminServer, tenant, tc.token, requireAdmin(adminServer)), "RequireAdmin")
			// Super admin is membership in the ADMIN account; a tenant identity never passes.
			assert.Equal(t, http.StatusForbidden, gate(t, adminServer, tenant, tc.token, requireSuperAdmin(adminServer)), "RequireSuperAdmin")
		})
	}

	// An untrusted-account or garbage token is still refused outright.
	assert.Equal(t, http.StatusUnauthorized, gate(t, adminServer, tenant, "not-a-jwt", requireAdmin(adminServer)))
}

func TestAgentConfigurationAppliesRolesAndPermissions(t *testing.T) {
	adminServer := newTestAdminServer(t, t.TempDir()+"/admin.db")
	tenant := makeAccount(t, adminServer, "tenant-config")
	st := adminServer.Store()

	perms := user.ReadOnlyAPIPermissions("admin_service")
	_, err := adminServer.AgentConfiguration(&user.AgentConfigurationOptions{
		AccountID: tenant, HostID: "host-cfg", Hostname: "cfg.local",
		Roles: []string{user.RoleViewer}, Permissions: perms,
	})
	// The test server has no intermediate CA, so configuration stops at
	// the certificate step; the agent record is created before that.
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "intermediate CA"), err.Error())

	agent, err := st.GetAgentByHostID(tenant, "host-cfg")
	require.NoError(t, err)
	assert.Equal(t, []string{user.RoleViewer}, agent.Roles)
	require.NotNil(t, agent.Permissions)
	assert.ElementsMatch(t, perms.PublishAllow, agent.Permissions.PublishAllow)
	claims, err := user.ValidateUserToken(agent.Token, tenant)
	require.NoError(t, err)
	assert.ElementsMatch(t, perms.PublishAllow, []string(claims.Permissions.Pub.Allow))
	firstToken := agent.Token

	// Re-registering without roles or permissions leaves the agent alone.
	_, _ = adminServer.AgentConfiguration(&user.AgentConfigurationOptions{AccountID: tenant, HostID: "host-cfg", Hostname: "cfg.local"})
	agent, err = st.GetAgentByHostID(tenant, "host-cfg")
	require.NoError(t, err)
	assert.Equal(t, firstToken, agent.Token)

	// Re-registering with roles updates the record and reissues the token.
	_, _ = adminServer.AgentConfiguration(&user.AgentConfigurationOptions{
		AccountID: tenant, HostID: "host-cfg", Hostname: "cfg.local",
		Roles: []string{user.RoleAdministrator}, Permissions: &user.NATSPermissions{},
	})
	agent, err = st.GetAgentByHostID(tenant, "host-cfg")
	require.NoError(t, err)
	assert.Equal(t, []string{user.RoleAdministrator}, agent.Roles)
	assert.Nil(t, agent.Permissions, "empty permissions clear the restriction")
	assert.NotEqual(t, firstToken, agent.Token)
	claims, err = user.ValidateUserToken(agent.Token, tenant)
	require.NoError(t, err)
	assert.Empty(t, claims.Permissions.Pub.Allow)
	assert.True(t, claims.Tags.Contains(user.RoleAdministrator))
	assert.Equal(t, http.StatusOK, gate(t, adminServer, tenant, agent.Token, requireAdmin(adminServer)))

	// Invalid roles are rejected before anything is written.
	_, err = adminServer.AgentConfiguration(&user.AgentConfigurationOptions{AccountID: tenant, HostID: "host-bad", Hostname: "bad", Roles: []string{"root"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid role")
	_, err = st.GetAgentByHostID(tenant, "host-bad")
	assert.Error(t, err)
}
