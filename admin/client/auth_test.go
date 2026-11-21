package client

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/platform/user"
	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStore is an in-memory IdentityStore for exercising the auth middleware.
type fakeStore struct {
	passwords     map[string]string        // accountID|userID -> valid password
	activities    map[string]user.Activity // accountID|userID -> activity
	usersByPubKey map[string]*user.User    // accountID|publicKey -> user
	validateErr   error                    // forced error from ValidateUserPassword
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		passwords:     map[string]string{},
		activities:    map[string]user.Activity{},
		usersByPubKey: map[string]*user.User{},
	}
}

func key(a, b string) string { return a + "|" + b }

func (f *fakeStore) GetUserByEmail(email string) (*user.User, string, error) {
	return nil, "", assertErr("GetUserByEmail not implemented")
}

func (f *fakeStore) GetUserByPublicKey(accountID, publicKey string) (*user.User, error) {
	if u, ok := f.usersByPubKey[key(accountID, publicKey)]; ok {
		return u, nil
	}
	return nil, assertErr("user not found")
}

func (f *fakeStore) GetActivity(accountID, activityID string) (user.Activity, error) {
	if a, ok := f.activities[key(accountID, activityID)]; ok {
		return a, nil
	}
	return user.Activity{}, assertErr("activity not found")
}

func (f *fakeStore) UpdateActivity(accountID string, a user.Activity) error {
	existing := f.activities[key(accountID, a.ID)]
	existing.ID = a.ID
	existing.FailedLoginCount += a.FailedLoginCount
	f.activities[key(accountID, a.ID)] = existing
	return nil
}

func (f *fakeStore) PutActivity(accountID string, activity user.Activity) error {
	f.activities[key(accountID, activity.ID)] = activity
	return nil
}

func (f *fakeStore) ValidateUserPassword(accountID, userID, password string) (bool, error) {
	if f.validateErr != nil {
		return false, f.validateErr
	}
	want, ok := f.passwords[key(accountID, userID)]
	if !ok {
		return false, nil
	}
	return want == password, nil
}

type stringErr string

func (e stringErr) Error() string { return string(e) }
func assertErr(s string) error    { return stringErr(s) }

func testLogger() *logger.Logger { return logger.NewLogger("test", "auth_test") }

// ctxReq builds a request whose context carries the ID-middleware values that
// the auth middleware reads. The auth middleware never sets these itself; the
// ID middleware runs first in production.
func ctxReq(accountID, accountPublicKey, userID string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := context.WithValue(r.Context(), idContextAccountID, accountID)
	ctx = context.WithValue(ctx, idContextAccountPublicKey, accountPublicKey)
	ctx = context.WithValue(ctx, idContextUserID, userID)
	return r.WithContext(ctx)
}

func basicHeader(usernmame, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(usernmame+":"+password))
}

// okHandler flips called and returns 200.
func okHandler(called *bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*called = true
		w.WriteHeader(http.StatusOK)
	}
}

// makeAccountUserToken mints an account keypair and a user, returning the
// account public key (issuer), the user's public key (claims.Subject), the
// user, and a signed bearer token valid for minutesValid.
func makeAccountUserToken(t *testing.T, roles []string, minutesValid int) (accountPK, userPK string, u *user.User, token string) {
	t.Helper()
	kp, err := nkeys.CreateAccount()
	require.NoError(t, err)
	accountPK, err = kp.PublicKey()
	require.NoError(t, err)
	u, _, err = user.NewUser(&user.UserConfig{
		Email:     "auth-user@test.local",
		Password:  "pw",
		Roles:     roles,
		AccountID: accountPK,
	}, kp)
	require.NoError(t, err)
	tok, err := user.NewUserToken(u, minutesValid, kp)
	require.NoError(t, err)
	return accountPK, u.PublicKey, u, tok.String()
}

func TestBasicAuth(t *testing.T) {
	const accountID, userID = "acctA", "userA"
	tests := []struct {
		name       string
		password   string
		locked     bool
		wantStatus int
		wantCalled bool
	}{
		{name: "valid password", password: "secret", wantStatus: http.StatusOK, wantCalled: true},
		{name: "invalid password", password: "wrong", wantStatus: http.StatusUnauthorized},
		{name: "account locked", password: "secret", locked: true, wantStatus: http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			store.passwords[key(accountID, userID)] = "secret"
			store.activities[key(accountID, userID)] = user.Activity{ID: userID, Locked: tc.locked}
			auth := NewAuth(AuthConfig{BasicAuth: true, Store: store, Logger: testLogger()})

			called := false
			r := ctxReq(accountID, accountID, userID)
			r.Header.Set("authorization", basicHeader("auth-user@test.local", tc.password))
			rr := httptest.NewRecorder()
			auth.Basic(okHandler(&called)).ServeHTTP(rr, r)

			assert.Equal(t, tc.wantStatus, rr.Code)
			assert.Equal(t, tc.wantCalled, called)
		})
	}
}

func TestBasicOrBearer_Basic(t *testing.T) {
	const accountID, userID = "acctA", "userA"
	store := newFakeStore()
	store.passwords[key(accountID, userID)] = "secret"
	store.activities[key(accountID, userID)] = user.Activity{ID: userID}
	auth := NewAuth(AuthConfig{BasicAuth: true, Store: store, Logger: testLogger()})

	t.Run("valid", func(t *testing.T) {
		called := false
		r := ctxReq(accountID, accountID, userID)
		r.Header.Set("authorization", basicHeader("auth-user@test.local", "secret"))
		rr := httptest.NewRecorder()
		auth.BasicOrBearer(okHandler(&called)).ServeHTTP(rr, r)
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.True(t, called)
	})

	t.Run("invalid", func(t *testing.T) {
		called := false
		r := ctxReq(accountID, accountID, userID)
		r.Header.Set("authorization", basicHeader("auth-user@test.local", "wrong"))
		rr := httptest.NewRecorder()
		auth.BasicOrBearer(okHandler(&called)).ServeHTTP(rr, r)
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
		assert.False(t, called)
	})

	t.Run("locked", func(t *testing.T) {
		store.activities[key(accountID, userID)] = user.Activity{ID: userID, Locked: true}
		called := false
		r := ctxReq(accountID, accountID, userID)
		r.Header.Set("authorization", basicHeader("auth-user@test.local", "secret"))
		rr := httptest.NewRecorder()
		auth.BasicOrBearer(okHandler(&called)).ServeHTTP(rr, r)
		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.False(t, called)
	})
}

func TestBearerTrust(t *testing.T) {
	accountPK, _, _, token := makeAccountUserToken(t, []string{user.RoleUser}, 60)

	t.Run("untrusted account rejected", func(t *testing.T) {
		auth := NewAuth(AuthConfig{Logger: testLogger()}) // no trusted accounts
		called := false
		r := ctxReq(accountPK, accountPK, "")
		r.Header.Set("authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		auth.Bearer(okHandler(&called)).ServeHTTP(rr, r)
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
		assert.False(t, called)
	})

	t.Run("trusted account accepted", func(t *testing.T) {
		auth := NewAuth(AuthConfig{Logger: testLogger(), TrustedAccounts: []string{accountPK}})
		called := false
		r := ctxReq(accountPK, accountPK, "")
		r.Header.Set("authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		auth.Bearer(okHandler(&called)).ServeHTTP(rr, r)
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.True(t, called)
	})
}

func TestRequireSuperAdmin(t *testing.T) {
	accountPK, _, _, token := makeAccountUserToken(t, nil, 60)
	auth := NewAuth(AuthConfig{Logger: testLogger(), TrustedAccounts: []string{accountPK}})

	t.Run("admin account allowed", func(t *testing.T) {
		called := false
		r := ctxReq(accountPK, accountPK, "")
		r.Header.Set("authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		auth.RequireSuperAdmin(accountPK, okHandler(&called)).ServeHTTP(rr, r)
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.True(t, called)
	})

	t.Run("other account forbidden", func(t *testing.T) {
		called := false
		r := ctxReq(accountPK, accountPK, "")
		r.Header.Set("authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		// A different admin account ID => caller is not the super admin.
		auth.RequireSuperAdmin("some-other-admin-account", okHandler(&called)).ServeHTTP(rr, r)
		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.False(t, called)
	})
}

func TestRequireRole(t *testing.T) {
	const adminAccountID = "ADMIN-ACCT"

	t.Run("super admin allowed without store lookup", func(t *testing.T) {
		accountPK, _, _, token := makeAccountUserToken(t, nil, 60)
		// Caller is in the ADMIN account (accountID == adminAccountID). We model
		// that by using the ADMIN account's own keypair as the account.
		auth := NewAuth(AuthConfig{Logger: testLogger(), TrustedAccounts: []string{accountPK}})
		called := false
		r := ctxReq(adminAccountID, accountPK, "")
		// The token's issuer must match the account public key used to validate.
		r.Header.Set("authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		auth.RequireRole(adminAccountID, user.RoleAdministrator, okHandler(&called)).ServeHTTP(rr, r)
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.True(t, called)
	})

	t.Run("user with role allowed", func(t *testing.T) {
		accountPK, userPK, u, token := makeAccountUserToken(t, []string{user.RoleAdministrator}, 60)
		store := newFakeStore()
		store.usersByPubKey[key(accountPK, userPK)] = u
		auth := NewAuth(AuthConfig{Logger: testLogger(), TrustedAccounts: []string{accountPK}})
		auth.SetStore(store)
		called := false
		r := ctxReq(accountPK, accountPK, "")
		r.Header.Set("authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		auth.RequireRole(adminAccountID, user.RoleAdministrator, okHandler(&called)).ServeHTTP(rr, r)
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.True(t, called)
	})

	t.Run("user without role forbidden", func(t *testing.T) {
		accountPK, userPK, u, token := makeAccountUserToken(t, []string{user.RoleViewer}, 60)
		store := newFakeStore()
		store.usersByPubKey[key(accountPK, userPK)] = u
		auth := NewAuth(AuthConfig{Logger: testLogger(), TrustedAccounts: []string{accountPK}})
		auth.SetStore(store)
		called := false
		r := ctxReq(accountPK, accountPK, "")
		r.Header.Set("authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		auth.RequireRole(adminAccountID, user.RoleAdministrator, okHandler(&called)).ServeHTTP(rr, r)
		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.False(t, called)
	})
}
