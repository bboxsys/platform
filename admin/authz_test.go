package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bboxsys/platform/user"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requestWithAccount builds a request whose context has been populated by the
// real ID middleware, exactly as it would be for a live request. The
// X-Blackbox-AccountID header is how the ID middleware learns the caller's
// account, so setting it here reproduces an authenticated caller in that
// account without minting a full bearer token.
func requestWithAccount(t *testing.T, adminServer *AdminServer, method, target, accountID string, body []byte, vars map[string]string) *http.Request {
	t.Helper()
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, target, bytes.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	r.Header.Set("X-Blackbox-AccountID", accountID)
	if vars != nil {
		r = mux.SetURLVars(r, vars)
	}
	return r
}

// serveWithID runs the handler behind the ID middleware so the handler sees the
// same context keys production does.
func serveWithID(adminServer *AdminServer, h http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
	idmw := NewID(IDConfig{Logger: adminServer.Logger})
	rr := httptest.NewRecorder()
	idmw.Middleware(http.HandlerFunc(h)).ServeHTTP(rr, r)
	return rr
}

// captureContextRequest returns a request that has passed through the ID
// middleware, so its context carries AccountID/UserID like a live request.
func captureContextRequest(t *testing.T, adminServer *AdminServer, accountID string) *http.Request {
	t.Helper()
	r := requestWithAccount(t, adminServer, http.MethodGet, "/", accountID, nil, nil)
	var captured *http.Request
	idmw := NewID(IDConfig{Logger: adminServer.Logger})
	idmw.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		captured = req
	})).ServeHTTP(httptest.NewRecorder(), r)
	require.NotNil(t, captured)
	return captured
}

// makeAccount creates a fresh tenant account and returns its ID.
func makeAccount(t *testing.T, adminServer *AdminServer, name string) string {
	t.Helper()
	acct, err := adminServer.Store().NewAccount(user.AccountConfig{
		Name:       name,
		OperatorID: adminServer.OperatorID(),
	})
	require.NoError(t, err)
	require.NotEmpty(t, acct.ID)
	return acct.ID
}

// TestResolveTargetAccountID exercises the cross-tenant guard helpers directly.
func TestResolveTargetAccountID(t *testing.T) {
	adminServer := newTestAdminServer(t, t.TempDir()+"/admin.db")
	adminAccountID := adminServer.AdminAccountID()
	tenantB := makeAccount(t, adminServer, "tenant-b")

	t.Run("super admin honors requested account", func(t *testing.T) {
		r := captureContextRequest(t, adminServer, adminAccountID)
		assert.True(t, adminServer.isSuperAdmin(r))
		assert.Equal(t, tenantB, adminServer.resolveTargetAccountID(r, tenantB))
	})

	t.Run("super admin with empty request falls back to own account", func(t *testing.T) {
		r := captureContextRequest(t, adminServer, adminAccountID)
		assert.Equal(t, adminAccountID, adminServer.resolveTargetAccountID(r, ""))
	})

	t.Run("tenant admin cannot target another account", func(t *testing.T) {
		r := captureContextRequest(t, adminServer, tenantB)
		assert.False(t, adminServer.isSuperAdmin(r))
		// Requesting the ADMIN account (or any other) is ignored: always scoped
		// to the caller's own account.
		assert.Equal(t, tenantB, adminServer.resolveTargetAccountID(r, adminAccountID))
		assert.Equal(t, tenantB, adminServer.resolveTargetAccountID(r, "some-other-account"))
	})
}

// TestNewUserCrossTenant proves that a tenant admin cannot create a user in
// another account, while a super admin can.
func TestNewUserCrossTenant(t *testing.T) {
	adminServer := newTestAdminServer(t, t.TempDir()+"/admin.db")
	adminAccountID := adminServer.AdminAccountID()
	tenantB := makeAccount(t, adminServer, "tenant-b")
	tenantC := makeAccount(t, adminServer, "tenant-c")

	t.Run("tenant admin body account_id is ignored", func(t *testing.T) {
		// Caller is a tenant admin in B, but asks to create the user in the
		// ADMIN account. The user must land in B, not ADMIN.
		body, err := json.Marshal(user.UserConfig{
			Email:     "cross-tenant@b.local",
			FirstName: "Cross",
			LastName:  "Tenant",
			Password:  "testing123",
			Roles:     []string{user.RoleUser},
			AccountID: adminAccountID,
		})
		require.NoError(t, err)

		r := requestWithAccount(t, adminServer, http.MethodPost, "/api/v1/users", tenantB, body, nil)
		rr := serveWithID(adminServer, adminServer.NewUser, r)
		require.Equal(t, http.StatusCreated, rr.Code, "body: %s", rr.Body.String())

		u, accountID, err := adminServer.Store().GetUserByEmail("cross-tenant@b.local")
		require.NoError(t, err)
		assert.Equal(t, tenantB, accountID, "user must be created in the caller's account, not the requested one")
		assert.NotEqual(t, adminAccountID, accountID)
		assert.Equal(t, tenantB, u.AccountID)
	})

	t.Run("super admin honors body account_id", func(t *testing.T) {
		// Caller is a super admin (ADMIN account) targeting tenant C.
		body, err := json.Marshal(user.UserConfig{
			Email:     "placed-by-super@c.local",
			FirstName: "Placed",
			LastName:  "BySuper",
			Password:  "testing123",
			Roles:     []string{user.RoleUser},
			AccountID: tenantC,
		})
		require.NoError(t, err)

		r := requestWithAccount(t, adminServer, http.MethodPost, "/api/v1/users", adminAccountID, body, nil)
		rr := serveWithID(adminServer, adminServer.NewUser, r)
		require.Equal(t, http.StatusCreated, rr.Code, "body: %s", rr.Body.String())

		_, accountID, err := adminServer.Store().GetUserByEmail("placed-by-super@c.local")
		require.NoError(t, err)
		assert.Equal(t, tenantC, accountID, "super admin must be able to place the user in the requested account")
	})
}

// TestGetInvitationsCrossTenant proves that a tenant admin cannot read another
// account's invitations by supplying ?account_id=OTHER.
func TestGetInvitationsCrossTenant(t *testing.T) {
	adminServer := newTestAdminServer(t, t.TempDir()+"/admin.db")
	adminAccountID := adminServer.AdminAccountID()
	tenantB := makeAccount(t, adminServer, "tenant-b")
	tenantC := makeAccount(t, adminServer, "tenant-c")

	invB, err := user.NewInvitation(&user.InvitationConfig{Email: "invitee-b@b.local", Roles: []string{user.RoleUser}}, tenantB, "inviter-b")
	require.NoError(t, err)
	require.NoError(t, adminServer.Store().PutInvitation(invB))

	invC, err := user.NewInvitation(&user.InvitationConfig{Email: "invitee-c@c.local", Roles: []string{user.RoleUser}}, tenantC, "inviter-c")
	require.NoError(t, err)
	require.NoError(t, adminServer.Store().PutInvitation(invC))

	decode := func(rr *httptest.ResponseRecorder) []*user.Invitation {
		var out []*user.Invitation
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
		return out
	}

	t.Run("tenant admin sees only its own invitations", func(t *testing.T) {
		r := requestWithAccount(t, adminServer, http.MethodGet, "/api/v1/invitations?account_id="+tenantC, tenantB, nil, nil)
		rr := serveWithID(adminServer, adminServer.GetInvitations, r)
		require.Equal(t, http.StatusOK, rr.Code)
		invs := decode(rr)
		require.Len(t, invs, 1)
		assert.Equal(t, tenantB, invs[0].AccountID)
		assert.Equal(t, "invitee-b@b.local", invs[0].Email)
	})

	t.Run("super admin honors requested account_id", func(t *testing.T) {
		r := requestWithAccount(t, adminServer, http.MethodGet, "/api/v1/invitations?account_id="+tenantB, adminAccountID, nil, nil)
		rr := serveWithID(adminServer, adminServer.GetInvitations, r)
		require.Equal(t, http.StatusOK, rr.Code)
		invs := decode(rr)
		require.Len(t, invs, 1)
		assert.Equal(t, tenantB, invs[0].AccountID)
	})
}

// TestUnlockUserCrossTenant proves a tenant admin cannot unlock users in
// another account, while its own account and super admins are allowed.
func TestUnlockUserCrossTenant(t *testing.T) {
	adminServer := newTestAdminServer(t, t.TempDir()+"/admin.db")
	adminAccountID := adminServer.AdminAccountID()
	tenantB := makeAccount(t, adminServer, "tenant-b")
	tenantC := makeAccount(t, adminServer, "tenant-c")

	// Seed a locked activity in tenant C.
	lockedUserID := "locked-user"
	require.NoError(t, adminServer.Store().PutActivity(tenantC, user.Activity{
		ID:               lockedUserID,
		Locked:           true,
		FailedLoginCount: 5,
	}))

	t.Run("tenant admin cannot unlock another account", func(t *testing.T) {
		vars := map[string]string{"accountID": tenantC, "userID": lockedUserID}
		r := requestWithAccount(t, adminServer, http.MethodPost, "/api/v1/accounts/"+tenantC+"/users/"+lockedUserID+"/unlock", tenantB, nil, vars)
		rr := serveWithID(adminServer, adminServer.UnlockUser, r)
		assert.Equal(t, http.StatusForbidden, rr.Code)

		// Activity in C must remain locked.
		act, err := adminServer.Store().GetActivity(tenantC, lockedUserID)
		require.NoError(t, err)
		assert.True(t, act.Locked, "cross-tenant unlock must not have taken effect")
	})

	t.Run("super admin can unlock another account", func(t *testing.T) {
		vars := map[string]string{"accountID": tenantC, "userID": lockedUserID}
		r := requestWithAccount(t, adminServer, http.MethodPost, "/api/v1/accounts/"+tenantC+"/users/"+lockedUserID+"/unlock", adminAccountID, nil, vars)
		rr := serveWithID(adminServer, adminServer.UnlockUser, r)
		require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

		act, err := adminServer.Store().GetActivity(tenantC, lockedUserID)
		require.NoError(t, err)
		assert.False(t, act.Locked)
		assert.Equal(t, 0, act.FailedLoginCount)
	})

	t.Run("tenant admin can unlock its own account", func(t *testing.T) {
		ownUserID := "own-user"
		require.NoError(t, adminServer.Store().PutActivity(tenantB, user.Activity{
			ID:     ownUserID,
			Locked: true,
		}))
		vars := map[string]string{"accountID": tenantB, "userID": ownUserID}
		r := requestWithAccount(t, adminServer, http.MethodPost, "/api/v1/accounts/"+tenantB+"/users/"+ownUserID+"/unlock", tenantB, nil, vars)
		rr := serveWithID(adminServer, adminServer.UnlockUser, r)
		require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

		act, err := adminServer.Store().GetActivity(tenantB, ownUserID)
		require.NoError(t, err)
		assert.False(t, act.Locked)
	})
}
