package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bboxsys/platform/user"
	"github.com/stretchr/testify/require"
)

// TestNoopWithBearer covers the session check every SPA and agent makes:
// after Bearer the context user is the token subject (a public key), so the
// handler must look the identity up by key, for users and agents alike.
func TestNoopWithBearer(t *testing.T) {
	adminServer := newTestAdminServer(t, t.TempDir()+"/admin.db")
	tenant := makeAccount(t, adminServer, "tenant")
	adminServer.AddHandlers()
	member := makeTokenTestUser(t, adminServer, tenant, "member@tenant.local", []string{user.RoleUser})

	call := func(bearer string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/noop", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("X-Blackbox-AccountID", tenant)
		rec := httptest.NewRecorder()
		adminServer.Router.ServeHTTP(rec, req)
		return rec.Code
	}
	require.Equal(t, http.StatusOK, call(member.bearer))

	// An agent's own JWT is a valid bearer and is found through the
	// identity fallback.
	agent, err := adminServer.Store().NewAgent(&user.UserConfig{AccountID: tenant, HostID: "host-1", Hostname: "host-1"})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, call(agent.Token))
}
