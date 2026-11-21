package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/bboxsys/platform/natsapi"
	"github.com/bboxsys/platform/user"
	natsd "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func startTestNATS(t *testing.T) *natsd.Server {
	t.Helper()
	srv, err := natsd.NewServer(&natsd.Options{Host: "127.0.0.1", Port: -1, NoSigs: true, NoLog: true})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(5*time.Second), "nats server did not start")
	t.Cleanup(srv.Shutdown)
	return srv
}

func natsConnect(t *testing.T, srv *natsd.Server) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

func natsCall(t *testing.T, client *http.Client, u tokenTestUser, method, path string) (*http.Response, []byte) {
	t.Helper()
	req, err := natsapi.NewRequest(context.Background(), method, AdminNATSService, path, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+u.bearer)
	resp, err := client.Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, body
}

// TestAdminNATSAPI serves the admin API on two NATS connections standing in
// for the admin account's and a tenant's collector connections, and checks
// that calls over NATS get exactly the REST auth tiers.
func TestAdminNATSAPI(t *testing.T) {
	adminServer := newTestAdminServer(t, t.TempDir()+"/admin.db")
	adminAccountID := adminServer.AdminAccountID()
	// Accounts created before AddHandlers are trusted by the auth wrappers.
	tenant := makeAccount(t, adminServer, "tenant")
	adminServer.AddHandlers()
	superAdmin := makeTokenTestUser(t, adminServer, adminAccountID, "root@admin.local", []string{user.RoleAdministrator})
	tenantAdmin := makeTokenTestUser(t, adminServer, tenant, "admin@tenant.local", []string{user.RoleAdministrator})
	tenantMember := makeTokenTestUser(t, adminServer, tenant, "member@tenant.local", []string{user.RoleUser})

	require.Nil(t, adminServer.NATSAPI(), "no bridge before StartNATSAPI")
	require.NoError(t, adminServer.StartNATSAPI(NATSAPIOptions{Description: "test"}))
	require.NoError(t, adminServer.StartNATSAPI(NATSAPIOptions{}), "StartNATSAPI must be idempotent")
	api := adminServer.NATSAPI()
	require.NotNil(t, api)
	require.Equal(t, AdminNATSService, api.Name())

	srv := startTestNATS(t)
	// Two embedded-server connections play the per-account collector
	// connections: a request arriving on one is pinned to its account.
	require.NoError(t, api.Serve(adminAccountID, natsConnect(t, srv)))
	t.Cleanup(adminServer.Close)
	adminClient := natsapi.NewClient(natsConnect(t, srv))

	// GET /api/v1/noop looks the bearer's subject up as a store ID and
	// answers 500 for any bearer, over HTTPS and NATS alike (pre-existing);
	// /api/v1/roles is the bearer-tier smoke check instead.
	resp, _ := natsCall(t, adminClient, superAdmin, "GET", "/api/v1/roles")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, body := natsCall(t, adminClient, superAdmin, "GET", "/api/v1/users")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var users []user.User
	require.NoError(t, json.Unmarshal(body, &users))
	require.NotEmpty(t, users, "the admin account's users stream back over NATS")
	for _, u := range users {
		require.Equal(t, adminAccountID, u.AccountID)
	}

	resp, body = natsCall(t, adminClient, superAdmin, "GET", "/api/v1/accounts")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	// A tenant token on the admin account's connection does not pass: the
	// bearer must be issued by the connection's account.
	resp, _ = natsCall(t, adminClient, tenantAdmin, "GET", "/api/v1/users")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// Stop the admin account and serve the tenant account instead, on a
	// fresh server so queue-group routing cannot pick the wrong one.
	require.NoError(t, api.Stop(adminAccountID))
	tenantSrv := startTestNATS(t)
	require.NoError(t, api.Serve(tenant, natsConnect(t, tenantSrv)))
	tenantClient := natsapi.NewClient(natsConnect(t, tenantSrv))

	resp, body = natsCall(t, tenantClient, tenantAdmin, "GET", "/api/v1/users")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	users = nil
	require.NoError(t, json.Unmarshal(body, &users))
	require.Len(t, users, 2)
	for _, u := range users {
		require.Equal(t, tenant, u.AccountID, "tenant sees only its own users")
	}

	// Permission parity: a tenant admin is not a super admin, a member is
	// not an admin, exactly as over HTTPS.
	resp, _ = natsCall(t, tenantClient, tenantAdmin, "GET", "/api/v1/accounts")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp, _ = natsCall(t, tenantClient, tenantMember, "GET", "/api/v1/users")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp, _ = natsCall(t, tenantClient, tenantMember, "GET", "/api/v1/roles")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Naming another account in the header is refused by the bridge.
	req, _ := natsapi.NewRequest(context.Background(), "GET", AdminNATSService, "/api/v1/users", nil)
	req.Header.Set("Authorization", "Bearer "+superAdmin.bearer)
	req.Header.Set(natsapi.HeaderAccountID, adminAccountID)
	resp, err := tenantClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	// Discovery answers inside the served account.
	info, err := natsConnect(t, tenantSrv).Request("$SRV.INFO."+AdminNATSService, nil, 2*time.Second)
	require.NoError(t, err)
	require.Contains(t, string(info.Data), `"name":"admin_service"`)

	adminServer.Close()
	require.Nil(t, adminServer.NATSAPI())
	_, err = tenantClient.Do(req)
	require.ErrorIs(t, err, natsapi.ErrNoService)
}

// TestStartCollectorsNeedsStoreOrNATSAPI checks the relaxed precondition:
// without stores and without the bridge the collectors refuse to start.
func TestStartCollectorsNeedsStoreOrNATSAPI(t *testing.T) {
	adminServer := newTestAdminServer(t, t.TempDir()+"/admin.db")
	t.Cleanup(adminServer.Close)
	_, err := adminServer.StartCollectors(CollectorOptions{})
	require.Error(t, err)
}
