package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bboxsys/platform/apicontract"
	"github.com/bboxsys/platform/user"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The list routes answer the complete sorted collection by default and a
// page object when the request names limit or offset, so the console and
// older clients keep their arrays while the assistant and scripts can page.

type testPage struct {
	apicontract.Page
	Items []json.RawMessage `json:"items"`
}

func listCall(t *testing.T, adminServer *AdminServer, h http.HandlerFunc, target, accountID string) *httptest.ResponseRecorder {
	t.Helper()
	return serveWithID(adminServer, h, requestWithAccount(t, adminServer, http.MethodGet, target, accountID, nil, nil))
}

func decodePage(t *testing.T, rr *httptest.ResponseRecorder) testPage {
	t.Helper()
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var page testPage
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &page), rr.Body.String())
	return page
}

func TestListRoutesPage(t *testing.T) {
	adminServer := newTestAdminServer(t, t.TempDir()+"/admin.db")
	adminAccountID := adminServer.AdminAccountID()
	tenant := makeAccount(t, adminServer, "tenant-paging")
	for _, email := range []string{"carol@test.local", "alice@test.local", "Bob@test.local"} {
		_, err := adminServer.Store().NewUser(&user.UserConfig{Email: email, Password: "pw", AccountID: tenant, Roles: []string{user.RoleUser}})
		require.NoError(t, err)
	}

	t.Run("complete array without paging, sorted", func(t *testing.T) {
		rr := listCall(t, adminServer, adminServer.GetUsers, "/api/v1/users?account_id="+tenant, adminAccountID)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))
		var users []user.User
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &users))
		emails := make([]string, 0, len(users))
		for _, u := range users {
			emails = append(emails, u.Email)
		}
		assert.Equal(t, []string{"alice@test.local", "Bob@test.local", "carol@test.local"}, emails, "case-insensitive by email")
	})

	t.Run("limit answers a page with totals", func(t *testing.T) {
		page := decodePage(t, listCall(t, adminServer, adminServer.GetUsers, "/api/v1/users?account_id="+tenant+"&limit=2", adminAccountID))
		assert.Equal(t, 3, page.Total)
		assert.Equal(t, 2, page.Limit)
		assert.Equal(t, 0, page.Offset)
		assert.True(t, page.HasMore)
		require.NotNil(t, page.NextOffset)
		assert.Equal(t, 2, *page.NextOffset)
		require.Len(t, page.Items, 2)

		next := decodePage(t, listCall(t, adminServer, adminServer.GetUsers, "/api/v1/users?account_id="+tenant+"&limit=2&offset=2", adminAccountID))
		assert.Equal(t, 3, next.Total)
		assert.False(t, next.HasMore)
		assert.Nil(t, next.NextOffset)
		require.Len(t, next.Items, 1)
		var last user.User
		require.NoError(t, json.Unmarshal(next.Items[0], &last))
		assert.Equal(t, "carol@test.local", last.Email)
	})

	t.Run("offset alone pages with the default limit", func(t *testing.T) {
		page := decodePage(t, listCall(t, adminServer, adminServer.GetUsers, "/api/v1/users?account_id="+tenant+"&offset=1", adminAccountID))
		assert.Equal(t, apicontract.DefaultPageLimit, page.Limit)
		assert.Len(t, page.Items, 2)
	})

	t.Run("offset past the end is an empty page", func(t *testing.T) {
		page := decodePage(t, listCall(t, adminServer, adminServer.GetUsers, "/api/v1/users?account_id="+tenant+"&limit=2&offset=10", adminAccountID))
		assert.Equal(t, 3, page.Total)
		assert.Empty(t, page.Items)
		assert.False(t, page.HasMore)
	})

	t.Run("out of range values are rejected", func(t *testing.T) {
		for _, q := range []string{"limit=0", "limit=201", "limit=ten", "offset=-1", "offset=x"} {
			rr := listCall(t, adminServer, adminServer.GetUsers, "/api/v1/users?account_id="+tenant+"&"+q, adminAccountID)
			assert.Equal(t, http.StatusBadRequest, rr.Code, q)
		}
	})

	t.Run("accounts by name", func(t *testing.T) {
		makeAccount(t, adminServer, "aardvark")
		rr := listCall(t, adminServer, adminServer.GetAccounts, "/api/v1/accounts", adminAccountID)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var accounts []user.Account
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &accounts))
		require.GreaterOrEqual(t, len(accounts), 3)
		assert.Equal(t, "aardvark", accounts[0].Name)
		page := decodePage(t, listCall(t, adminServer, adminServer.GetAccounts, "/api/v1/accounts?limit=1", adminAccountID))
		assert.Equal(t, len(accounts), page.Total)
		assert.Len(t, page.Items, 1)
	})

	t.Run("empty collections are arrays and empty pages", func(t *testing.T) {
		rr := listCall(t, adminServer, adminServer.GetAgents, "/api/v1/agents", tenant)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.Equal(t, "[]\n", rr.Body.String())
		page := decodePage(t, listCall(t, adminServer, adminServer.GetInvitations, "/api/v1/invitations?limit=5", tenant))
		assert.Equal(t, 0, page.Total)
		assert.NotNil(t, page.Items)
		assert.Empty(t, page.Items)
	})

	t.Run("service endpoints keep the map and page as a list", func(t *testing.T) {
		require.NoError(t, adminServer.Store().PutServiceEndpoint(ServiceEndpoint{Kind: "zeta_service", URL: "https://zeta.test:1"}))
		require.NoError(t, adminServer.Store().PutServiceEndpoint(ServiceEndpoint{Kind: "alpha_service", URL: "https://alpha.test:1"}))
		rr := listCall(t, adminServer, adminServer.GetServiceEndpoints, "/api/v1/serviceendpoints", adminAccountID)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var byKind map[string]ServiceEndpoint
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &byKind))
		assert.Contains(t, byKind, "alpha_service")

		page := decodePage(t, listCall(t, adminServer, adminServer.GetServiceEndpoints, "/api/v1/serviceendpoints?limit=1", adminAccountID))
		assert.Equal(t, len(byKind), page.Total)
		require.Len(t, page.Items, 1)
		var first ServiceEndpoint
		require.NoError(t, json.Unmarshal(page.Items[0], &first))
		assert.Equal(t, "alpha_service", first.Kind)
	})
}

func TestAIToolGuidanceNamesAdminPaging(t *testing.T) {
	assert.Contains(t, aiToolGuidance("/api/v1/users"), "limit")
	assert.Contains(t, aiToolGuidance("/api/v1/users"), "next_offset")
	assert.Contains(t, aiToolGuidance("/api/v1/logs"), "since")
	assert.Contains(t, aiToolGuidance("/api/v1/ai/usage/runs"), "/api/v1/ai/usage/summary")
}
