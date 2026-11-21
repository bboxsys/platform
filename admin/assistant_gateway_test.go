package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bboxsys/platform/ai"
	"github.com/bboxsys/platform/ai/instance"
	"github.com/bboxsys/platform/ai/instance/natshost"
	"github.com/bboxsys/platform/user"
	"github.com/gorilla/mux"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// TestAssistantGateway relays console conversations to a service assistant
// hosted over NATS, on a connection inside the caller's account, and keeps
// everything but the conversation API out of reach.
func TestAssistantGateway(t *testing.T) {
	adminServer := newTestAdminServer(t, t.TempDir()+"/admin.db")
	tenant := makeAccount(t, adminServer, "tenant")
	srv := startTestNATS(t)
	connects := 0
	g := adminServer.MountAssistantGateway(AssistantGatewayOptions{
		Connect: func(accountID string) (*nats.Conn, func(), error) {
			connects++
			require.Equal(t, tenant, accountID, "connections live in the caller's account")
			nc := natsConnect(t, srv)
			return nc, nc.Close, nil
		},
	})
	require.Same(t, g, adminServer.MountAssistantGateway(AssistantGatewayOptions{}), "idempotent")
	adminServer.AddHandlers()
	t.Cleanup(adminServer.Close)
	member := makeTokenTestUser(t, adminServer, tenant, "member@tenant.local", []string{user.RoleUser})

	// The service: an instance over NATS whose tools are its own routes.
	router := mux.NewRouter()
	router.HandleFunc("/api/v1/things/catalog", func(http.ResponseWriter, *http.Request) {}).Methods(http.MethodGet)
	spec, include, err := instance.RouteTools(instance.RouteToolsOptions{Router: router, Root: "/api/v1/things"})
	require.NoError(t, err)
	auth := NewAuth(AuthConfig{Logger: adminServer.Logger, TrustedAccounts: []string{tenant}})
	host, err := natshost.New(natshost.Options{
		Instance: instance.Options{Name: "things_service", Dir: t.TempDir(), ToolPrefix: "things_", Credentials: ai.StaticCredentials{}, OpenAPI: spec, Handler: router, ToolFilter: include},
		Auth:     auth, AdminAccountID: adminServer.AdminAccountID(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { host.Close() })
	stops := 0
	require.Error(t, g.Register(AssistantService{ID: "admin"}), "the admin's own id is reserved")
	require.Error(t, g.Register(AssistantService{ID: "Things"}), "ids are lower case")
	require.NoError(t, g.Register(AssistantService{ID: "things", Serve: host.Serve, Stop: func(accountID string) { stops++; host.Stop(accountID) }}))
	require.Equal(t, []AssistantServiceInfo{{ID: "admin", Name: "Admin"}, {ID: "things", Name: "Things"}}, g.Services())

	session := &ai.Session{ID: "saved-session", AccountID: tenant, UserID: member.user.PublicKey, Title: "Things conversation"}
	require.NoError(t, ai.NewStoreSessions(host.Instance().Store()).Put(context.Background(), session))

	call := func(path string, spoof bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer "+member.bearer)
		r.Header.Set("X-Blackbox-AccountID", tenant)
		if spoof {
			r.Header.Set("X-Blackbox-UserID", "spoofed-user")
		}
		rr := httptest.NewRecorder()
		adminServer.Srv.Handler.ServeHTTP(rr, r)
		return rr
	}
	for _, tc := range []struct {
		path     string
		status   int
		contains string
	}{
		{AssistantServicesPath, http.StatusOK, "Things"},
		{AssistantServicesPath + "/things/status", http.StatusOK, `"enabled":false`},
		{AssistantServicesPath + "/things/sessions/saved-session", http.StatusOK, "Things conversation"},
		{AssistantServicesPath + "/things/sessions/missing", http.StatusNotFound, ""},
		{AssistantServicesPath + "/things/documents", http.StatusNotFound, ""},
		// The router normalizes dot segments with a redirect; the relay never sees them.
		{AssistantServicesPath + "/things/sessions/../documents", http.StatusMovedPermanently, ""},
		{AssistantServicesPath + "/unknown/status", http.StatusServiceUnavailable, ""},
	} {
		rr := call(tc.path, true)
		require.Equal(t, tc.status, rr.Code, tc.path)
		require.Contains(t, rr.Body.String(), tc.contains, tc.path)
	}
	require.Equal(t, 1, connects, "one connection per account")
	require.Equal(t, []string{tenant}, host.Accounts())

	// Without a bearer nothing is listed or relayed.
	rr := httptest.NewRecorder()
	adminServer.Srv.Handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, AssistantServicesPath, nil))
	require.Equal(t, http.StatusUnauthorized, rr.Code)

	var listed []AssistantServiceInfo
	require.NoError(t, json.Unmarshal(call(AssistantServicesPath, false).Body.Bytes(), &listed))
	require.Len(t, listed, 2)

	// Removing the account stops the service there and drops the connection.
	g.RemoveAccount(tenant)
	require.Equal(t, 1, stops)
	require.Empty(t, host.Accounts())
	require.Equal(t, http.StatusOK, call(AssistantServicesPath+"/things/status", false).Code, "the next request reconnects")
	require.Equal(t, 2, connects)

	g.Unregister("things")
	require.Equal(t, 2, stops)
	require.Equal(t, http.StatusServiceUnavailable, call(AssistantServicesPath+"/things/status", false).Code)
	require.Equal(t, []AssistantServiceInfo{{ID: "admin", Name: "Admin"}}, g.Services())
}
