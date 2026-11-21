package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
)

// TestAdminUIDoesNotShadowAPIRoutes verifies that wiring the admin SPA does not
// shadow API routes: a GET to an API endpoint must reach its handler (returning
// JSON), not the SPA's index.html, while unknown paths and assets still fall to
// the SPA. Regression for the catch-all PathPrefix("/") that returned
// index.html for GET /api/v1/login.
func TestAdminUIDoesNotShadowAPIRoutes(t *testing.T) {
	adminServer := newTestAdminServer(t, t.TempDir()+"/admin.db")

	uiFS := fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html><title>Admin</title>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	adminServer.ServeUI(uiFS)
	adminServer.AddHandlers()

	// GET /api/v1/login must reach the login handler (401 without credentials),
	// NOT be swallowed by the SPA.
	rec := httptest.NewRecorder()
	adminServer.Router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/login", nil))
	assert.NotContains(t, rec.Body.String(), "<!doctype", "the login API must not be shadowed by the SPA")

	// An unknown client-side route falls back to index.html for SPA routing.
	rec = httptest.NewRecorder()
	adminServer.Router.ServeHTTP(rec, httptest.NewRequest("GET", "/accounts", nil))
	assert.Contains(t, rec.Body.String(), "<!doctype")

	// A real static asset is served from the embedded FS.
	rec = httptest.NewRecorder()
	adminServer.Router.ServeHTTP(rec, httptest.NewRequest("GET", "/assets/app.js", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "console.log")
}
