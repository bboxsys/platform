package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bboxsys/platform/ai"
	"github.com/bboxsys/platform/ai/anthropic"
	"github.com/bboxsys/platform/ai/instance"
	"github.com/bboxsys/platform/user"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeModelsAPI answers GET /v1/models/{id} like the Anthropic Models API:
// 401 for any key but good, 404 for any model but claude-opus-5-5.
func fakeModelsAPI(t *testing.T, good string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Api-Key") != good {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
			return
		}
		model := strings.TrimPrefix(r.URL.Path, "/v1/models/")
		if model != "claude-opus-5-5" {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"type":"error","error":{"type":"not_found_error","message":"model: %s"}}`, model)
			return
		}
		fmt.Fprint(w, `{"type":"model","id":"claude-opus-5-5","display_name":"Claude Opus 5.5","created_at":"2026-09-01T00:00:00Z","max_input_tokens":1000000,"max_tokens":128000}`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestTestAICredential(t *testing.T) {
	adminServer := newTestAdminServer(t, t.TempDir()+"/admin.db")
	t.Cleanup(adminServer.Close)
	tenant := makeAccount(t, adminServer, "tenant")
	adminServer.AddHandlers()
	tenantAdmin := makeTokenTestUser(t, adminServer, tenant, "admin@tenant.local", []string{user.RoleAdministrator})
	// The real Anthropic provider, pointed at a fake Models API.
	require.NoError(t, adminServer.StartAI(AIOptions{
		Dir:       t.TempDir(),
		Providers: map[string]ai.ProviderFactory{ai.ProviderAnthropic: anthropic.Factory(anthropic.Options{})},
	}))
	endpoint := fakeModelsAPI(t, "sk-ant-good-key")

	do := func(method, target, body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		adminServer.Srv.Handler.ServeHTTP(rr, tokenRequest(t, tenant, tenantAdmin, method, target, body, nil))
		return rr
	}
	store := func(name, key, model string) {
		body := fmt.Sprintf(`{"name":%q,"type":"bearer","token":%q,"metadata":{"resource_type":"ai_model","endpoint":%q,"tags":{"ai_provider":"anthropic","ai_model":%q}}}`, name, key, endpoint, model)
		rr := do(http.MethodPost, "/api/v1/accounts/me/apitokens", body)
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	}
	test := func(name string) instance.CheckResult {
		rr := do(http.MethodPost, "/api/v1/ai/credentials/test", fmt.Sprintf(`{"name":%q,"kind":"ai_model"}`, name))
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.NotContains(t, rr.Body.String(), "sk-ant-", "the key never reaches the answer")
		var out instance.CheckResult
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
		return out
	}

	store("good", "sk-ant-good-key", "claude-opus-5-5")
	store("badkey", "sk-ant-wrong-key", "claude-opus-5-5")
	store("badmodel", "sk-ant-good-key", "claude-nonexistent")

	out := test("good")
	assert.True(t, out.OK, out.Message)
	assert.Equal(t, "Claude Opus 5.5", out.DisplayName)
	assert.EqualValues(t, 1000000, out.ContextWindow)
	assert.EqualValues(t, 128000, out.MaxOutput)

	out = test("badkey")
	assert.False(t, out.OK)
	assert.Equal(t, "auth", out.ErrorKind)
	assert.Contains(t, out.Message, "invalid x-api-key")

	out = test("badmodel")
	assert.False(t, out.OK)
	assert.Equal(t, "not_found", out.ErrorKind)
	assert.Contains(t, out.Message, "claude-nonexistent")

	rr := do(http.MethodPost, "/api/v1/ai/credentials/test", `{"name":"missing","kind":"ai_model"}`)
	assert.Equal(t, http.StatusNotFound, rr.Code)
	rr = do(http.MethodPost, "/api/v1/ai/credentials/test", `{"name":"good","kind":"bogus"}`)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}
