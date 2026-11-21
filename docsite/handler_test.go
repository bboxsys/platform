package docsite

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bboxsys/platform/user"
	"github.com/nats-io/nkeys"
)

type trust map[string]bool

func (t trust) IsPublicKeyTrusted(pk string) bool { return t[pk] }

// users is a UserLookup over the users minted in a test.
type users map[string]*user.User

func (u users) GetUserByPublicKey(accountID, publicKey string) (*user.User, error) {
	if x := u[accountID+"/"+publicKey]; x != nil {
		return x, nil
	}
	return nil, errors.New("not found")
}

var minted = users{}

// newAccount makes an account key pair and returns it with its public key.
func newAccount(t *testing.T) (nkeys.KeyPair, string) {
	t.Helper()
	acct, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatal(err)
	}
	apk, _ := acct.PublicKey()
	return acct, apk
}

// mintToken issues a user JWT from acct and records the user for lookup.
func mintToken(t *testing.T, acct nkeys.KeyPair, roles []string, ttl time.Duration) string {
	t.Helper()
	apk, _ := acct.PublicKey()
	u, _, err := user.NewUser(&user.UserConfig{Email: "reader@example.com", Password: "pw", Roles: roles, AccountID: apk}, acct)
	if err != nil {
		t.Fatal(err)
	}
	minted[apk+"/"+u.PublicKey] = u
	claims := user.NewUserClaims(u)
	if ttl != 0 {
		claims.Expires = time.Now().Add(ttl).Unix()
	}
	token, err := claims.Encode(acct)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func newTestHandler(t *testing.T, trusted trust, authorize func(*http.Request, *Identity) bool) http.Handler {
	t.Helper()
	site, err := Load(fixtureFS(), Options{Project: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := Handler(site, HandlerOptions{
		Prefix:    "/docs",
		Auth:      trusted,
		Users:     minted,
		Authorize: authorize,
		SignIn:    func(next string) string { return "/session/docs?next=" + next },
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func openSession(t *testing.T, h http.Handler, token, account string) *http.Cookie {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/docs/_session", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if account != "" {
		req.Header.Set(AccountHeader, account)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("session: %d %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies %v", cookies)
	}
	return cookies[0]
}

func TestHandlerGate(t *testing.T) {
	acct, account := newAccount(t)
	token := mintToken(t, acct, nil, time.Hour)
	h := newTestHandler(t, trust{account: true}, nil)

	// The bare prefix redirects onto the site root.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/docs", nil))
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/docs/" {
		t.Errorf("prefix: %d %s", rec.Code, rec.Header().Get("Location"))
	}

	// A page without a session goes to the gate with the path it wanted.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/docs/planning/rule-of-55/?q=1", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/session/docs?next=/docs/planning/rule-of-55/?q=1" {
		t.Errorf("gate: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	// An asset without a session is refused, never redirected to HTML.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/docs/_astro/app.abc123.js", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("asset without session: %d", rec.Code)
	}

	// Establishing a session needs a valid bearer from a trusted account.
	for name, set := range map[string]func(*http.Request){
		"no bearer": func(*http.Request) {},
		"garbage":   func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") },
		"wrong account": func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set(AccountHeader, "AOTHER")
		},
	} {
		req := httptest.NewRequest(http.MethodPost, "/docs/_session", nil)
		set(req)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	otherAcct, _ := newAccount(t)
	other := mintToken(t, otherAcct, nil, time.Hour)
	req := httptest.NewRequest(http.MethodPost, "/docs/_session", nil)
	req.Header.Set("Authorization", "Bearer "+other)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("untrusted issuer: %d", rec.Code)
	}
	expired := mintToken(t, acct, nil, -time.Minute)
	req = httptest.NewRequest(http.MethodPost, "/docs/_session", nil)
	req.Header.Set("Authorization", "Bearer "+expired)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expired token: %d", rec.Code)
	}

	cookie := openSession(t, h, token, account)
	if cookie.Name != DefaultCookieName || !cookie.HttpOnly || cookie.Path != "/docs" || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge <= 0 || cookie.MaxAge > 3600 {
		t.Errorf("cookie %+v", cookie)
	}
	if strings.Contains(cookie.Value, token) {
		t.Error("cookie must not carry the bearer")
	}

	get := func(path string, hdr ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(cookie)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := get("/docs/_session"); rec.Code != http.StatusNoContent {
		t.Errorf("session probe: %d", rec.Code)
	}
	if rec := get("/docs/"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Research notes") || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Errorf("root: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := get("/docs/planning/rule-of-55"); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/docs/planning/rule-of-55/" {
		t.Errorf("trailing slash: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := get("/docs/planning/rule-of-55/"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "The Rule of 55") || rec.Header().Get("Cache-Control") != "private, no-cache" {
		t.Errorf("page: %d %s", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if rec := get("/docs/missing/"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Not found") {
		t.Errorf("404 page: %d %s", rec.Code, rec.Body.String())
	}
	if rec := get("/docs/_astro/app.abc123.js", "Accept-Encoding", "gzip, br"); rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" || rec.Body.String() != "gz-bytes" || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("asset: %d %s %q", rec.Code, rec.Header().Get("Content-Encoding"), rec.Body.String())
	}
	if rec := get("/docs/_astro/app.abc123.js"); rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != "console.log(1)" {
		t.Errorf("plain asset: %d %q", rec.Code, rec.Body.String())
	}
	if rec := get("/docs/_astro/app.abc123.js.gz"); rec.Code != http.StatusNotFound {
		t.Errorf("compressed variant served directly: %d", rec.Code)
	}
	if rec := get("/docs/../etc/passwd"); rec.Code == http.StatusOK {
		t.Errorf("traversal: %d", rec.Code)
	}

	// Ending the session sends the next page request back to the gate.
	req = httptest.NewRequest(http.MethodDelete, "/docs/_session", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || rec.Result().Cookies()[0].MaxAge >= 0 {
		t.Errorf("delete: %d %+v", rec.Code, rec.Result().Cookies())
	}
	if rec := get("/docs/"); rec.Code != http.StatusFound {
		t.Errorf("after delete: %d", rec.Code)
	}
}

func TestHandlerAuthorize(t *testing.T) {
	acct, account := newAccount(t)
	admin := mintToken(t, acct, []string{"administrator"}, time.Hour)
	plain := mintToken(t, acct, nil, time.Hour)
	// Both users are in the trusted account; only the role (looked up
	// through Users, since the token carries none) admits.
	h := newTestHandler(t, trust{account: true}, func(_ *http.Request, id *Identity) bool {
		return user.HasRole(id.Roles, "administrator")
	})
	req := httptest.NewRequest(http.MethodPost, "/docs/_session", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("plain user: %d", rec.Code)
	}
	cookie := openSession(t, h, admin, "")
	req = httptest.NewRequest(http.MethodGet, "/docs/", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("admin page: %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "Acme") {
		t.Errorf("body %q", body)
	}
}

func TestSessionsBounded(t *testing.T) {
	now := time.Now()
	s := newSessions(3, func() time.Time { return now })
	var keys []string
	for i := 0; i < 5; i++ {
		k, err := s.add(&Identity{UserID: "u", Expires: now.Add(time.Duration(i+1) * time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	if len(s.byID) > 3 {
		t.Errorf("table not bounded: %d", len(s.byID))
	}
	if s.get(keys[4]) == nil {
		t.Error("newest session evicted")
	}
	now = now.Add(10 * time.Minute)
	if s.get(keys[4]) != nil {
		t.Error("expired session still live")
	}
}
