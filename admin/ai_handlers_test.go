package admin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bboxsys/platform/ai"
	"github.com/bboxsys/platform/ai/apitool"
	"github.com/bboxsys/platform/ai/instance"
	"github.com/bboxsys/platform/ai/mcp"
	"github.com/bboxsys/platform/ai/memory"
	"github.com/bboxsys/platform/ai/rag"
	"github.com/bboxsys/platform/user"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aiFakeStep is one scripted model turn.
type aiFakeStep struct {
	text  string
	calls []ai.ToolCall
}

// aiFakeProvider replays scripted turns; a step with calls stops with
// tool_use, any other with end_turn.
type aiFakeProvider struct {
	mu     sync.Mutex
	steps  []aiFakeStep
	cred   ai.Credential
	inputs []ai.TurnInput
}

func (p *aiFakeProvider) script(steps ...aiFakeStep) {
	p.mu.Lock()
	p.steps = steps
	p.mu.Unlock()
}

func (p *aiFakeProvider) Name() string  { return "fake" }
func (p *aiFakeProvider) Model() string { return "fake-1" }

func (p *aiFakeProvider) UserMessage(text string) ai.Message {
	return ai.Message{Role: ai.RoleUser, Content: json.RawMessage(fmt.Sprintf(`{"text":%q}`, text)), Text: text}
}

func (p *aiFakeProvider) ToolResultMessage(results []ai.ToolResult) ai.Message {
	b, _ := json.Marshal(results)
	return ai.Message{Role: ai.RoleUser, Content: b}
}

func (p *aiFakeProvider) Turn(_ context.Context, in ai.TurnInput, emit func(ai.Event)) (*ai.Turn, error) {
	p.mu.Lock()
	p.inputs = append(p.inputs, in)
	if len(p.steps) == 0 {
		p.mu.Unlock()
		return nil, errors.New("script exhausted")
	}
	step := p.steps[0]
	p.steps = p.steps[1:]
	p.mu.Unlock()
	if emit != nil && step.text != "" {
		emit(ai.Event{Type: ai.EventText, Text: step.text})
	}
	t := &ai.Turn{
		Text:       step.text,
		ToolCalls:  step.calls,
		StopReason: "end_turn",
		Usage:      ai.Usage{InputTokens: 10, OutputTokens: 5},
		Message:    ai.Message{Role: ai.RoleAssistant, Content: json.RawMessage(`{"assistant":true}`), Text: step.text, ToolCalls: step.calls},
	}
	if len(step.calls) > 0 {
		t.StopReason = ai.StopReasonToolUse
	}
	return t, nil
}

func aiCall(id, name, input string) ai.ToolCall {
	return ai.ToolCall{ID: id, Name: name, Input: json.RawMessage(input)}
}

// parseAISSE decodes the event frames of an SSE body.
func parseAISSE(t *testing.T, body string) []ai.Event {
	t.Helper()
	var events []ai.Event
	var data []string
	flush := func() {
		if len(data) > 0 {
			var e ai.Event
			require.NoError(t, json.Unmarshal([]byte(strings.Join(data, "\n")), &e), "frame %q", data)
			events = append(events, e)
		}
		data = nil
	}
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "data: "):
			data = append(data, strings.TrimPrefix(line, "data: "))
		}
	}
	flush()
	return events
}

func aiEventTypes(events []ai.Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, string(e.Type))
	}
	return out
}

func TestAIHandlers(t *testing.T) {
	adminServer := newTestAdminServer(t, t.TempDir()+"/admin.db")
	t.Cleanup(adminServer.Close)
	adminAccountID := adminServer.AdminAccountID()
	tenant := makeAccount(t, adminServer, "tenant")
	// AddHandlers builds the auth and trusts the accounts that exist; the
	// API tools dispatch through the full handler chain it registers.
	adminServer.AddHandlers()
	tenantAdmin := makeTokenTestUser(t, adminServer, tenant, "admin@tenant.local", []string{user.RoleAdministrator})
	member := makeTokenTestUser(t, adminServer, tenant, "member@tenant.local", []string{user.RoleUser})

	provider := &aiFakeProvider{}
	factory := func(cred ai.Credential) (ai.Provider, error) {
		provider.mu.Lock()
		provider.cred = cred
		provider.mu.Unlock()
		return provider, nil
	}
	require.Nil(t, adminServer.AI(), "no service before StartAI")
	// The usage store first, so the assistant reports into it in-process.
	require.NoError(t, adminServer.InitAIUsageStore(t.TempDir()+"/ai_usage.db", 0))
	opts := AIOptions{Dir: t.TempDir(), Providers: map[string]ai.ProviderFactory{ai.ProviderAnthropic: factory}}
	require.NoError(t, adminServer.StartAI(opts))
	require.NoError(t, adminServer.StartAI(opts), "StartAI must be idempotent")
	require.NotNil(t, adminServer.AI())

	// Requests go through the server's own handler chain (ID middleware,
	// router, Bearer wrappers), exactly as over HTTPS.
	do := func(u tokenTestUser, method, target, body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		adminServer.Srv.Handler.ServeHTTP(rr, tokenRequest(t, tenant, u, method, target, body, nil))
		return rr
	}
	_ = adminAccountID

	var session ai.Session
	t.Run("status before a model credential", func(t *testing.T) {
		rr := do(tenantAdmin, http.MethodGet, "/api/v1/ai/status", "")
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var st ai.Status
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &st))
		assert.False(t, st.Enabled)
		assert.Nil(t, st.Model)
		assert.Nil(t, st.Embedding)
		assert.False(t, st.AllowWrites)
		assert.Contains(t, rr.Body.String(), `"model":null`)
		assert.Contains(t, st.Tools, "admin_list_roles")
		assert.Contains(t, st.Tools, "admin_create_user")
		assert.Contains(t, st.Tools, "admin_api_request")
		assert.Contains(t, st.Tools, "memory")
		assert.NotContains(t, st.Tools, "search_documents", "no embedding credential, no search")
		for _, name := range st.Tools {
			assert.NotContains(t, name, "_ai_", "the assistant's own routes are not tools: %s", name)
			assert.NotEqual(t, "admin_mcp", name)
		}
		assert.Equal(t, []string{}, st.MCPServers)

		rr = do(tenantAdmin, http.MethodPost, "/api/v1/ai/sessions", `{"title":"x"}`)
		assert.Equal(t, http.StatusPreconditionFailed, rr.Code)
		assert.JSONEq(t, `{"error":"no AI model credential configured"}`, rr.Body.String())

		rr = do(tenantAdmin, http.MethodGet, "/api/v1/ai/documents/search?q=x", "")
		assert.Equal(t, http.StatusPreconditionFailed, rr.Code, "no embedding credential")
		rr = do(tenantAdmin, http.MethodPost, "/api/v1/ai/documents", `{"documents":[{"id":"a","content":"b"}]}`)
		assert.Equal(t, http.StatusPreconditionFailed, rr.Code, "no embedding credential")
		rr = do(member, http.MethodPost, "/api/v1/ai/documents", `{"documents":[{"id":"a","content":"b"}]}`)
		assert.Equal(t, http.StatusForbidden, rr.Code, "documents are administrator only")

		rr = httptest.NewRecorder()
		adminServer.Srv.Handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/ai/status", nil))
		assert.Equal(t, http.StatusUnauthorized, rr.Code, "bearer required")
	})

	t.Run("status after storing an ai_model token", func(t *testing.T) {
		body := `{"name":"claude","type":"bearer","token":"sk-test-key","metadata":{"resource_type":"ai_model","tags":{"ai_provider":"anthropic","ai_model":"claude-test"}}}`
		rr := do(tenantAdmin, http.MethodPost, "/api/v1/accounts/me/apitokens", body)
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())

		rr = do(member, http.MethodGet, "/api/v1/ai/status", "")
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var st ai.Status
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &st))
		assert.True(t, st.Enabled)
		require.NotNil(t, st.Model)
		assert.Equal(t, ai.CredentialRef{Name: "claude", Provider: "anthropic", Model: "claude-test"}, *st.Model)
		assert.NotContains(t, rr.Body.String(), "sk-test-key")
	})

	t.Run("create session", func(t *testing.T) {
		rr := do(tenantAdmin, http.MethodPost, "/api/v1/ai/sessions", `{"title":"roles"}`)
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &session))
		assert.NotEmpty(t, session.ID)
		assert.Equal(t, tenant, session.AccountID)
		assert.Equal(t, tenantAdmin.user.PublicKey, session.UserID)
		assert.Equal(t, "claude", session.Credential)
		assert.Equal(t, "fake", session.Provider)
		assert.Equal(t, "sk-test-key", provider.cred.Bearer, "the factory receives the unsealed bearer")

		rr = do(tenantAdmin, http.MethodGet, "/api/v1/ai/sessions", "")
		require.Equal(t, http.StatusOK, rr.Code)
		var list []ai.SessionSummary
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
		require.Len(t, list, 1)
		assert.Equal(t, "roles", list[0].Title)
	})

	t.Run("send a message that calls a read-only api tool", func(t *testing.T) {
		provider.script(
			aiFakeStep{text: "Checking roles", calls: []ai.ToolCall{aiCall("c1", "admin_list_roles", `{}`)}},
			aiFakeStep{text: "Roles listed"},
		)
		rr := do(tenantAdmin, http.MethodPost, "/api/v1/ai/sessions/"+session.ID+"/messages", `{"content":"what roles exist?"}`)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.Equal(t, "text/event-stream", rr.Header().Get("Content-Type"))
		events := parseAISSE(t, rr.Body.String())
		assert.Equal(t, []string{"text", "usage", "tool_use", "tool_result", "text", "usage", "done"}, aiEventTypes(events))
		assert.Equal(t, "Checking roles", events[0].Text)
		require.NotNil(t, events[2].Call)
		assert.Equal(t, "admin_list_roles", events[2].Call.Name)
		require.NotNil(t, events[3].Result)
		assert.False(t, events[3].Result.Denied)
		assert.False(t, events[3].Result.Result.IsError, events[3].Result.Result.Content)
		for _, role := range user.AllRoles() {
			assert.Contains(t, events[3].Result.Result.Content, role, "the tool went through the router and returned the roles")
		}
		assert.Equal(t, "end_turn", events[6].StopReason)
		assert.Equal(t, int64(10), events[1].Usage.InputTokens)

		rr = do(tenantAdmin, http.MethodGet, "/api/v1/ai/sessions/"+session.ID, "")
		require.Equal(t, http.StatusOK, rr.Code)
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &session))
		require.Len(t, session.Messages, 4)
		assert.Equal(t, ai.RoleUser, session.Messages[0].Role)
		assert.Equal(t, "what roles exist?", session.Messages[0].Text)
		assert.Equal(t, ai.RoleAssistant, session.Messages[1].Role)
		require.Len(t, session.Messages[2].ToolResults, 1)
		assert.Contains(t, session.Messages[2].ToolResults[0].Result.Content, user.RoleAdministrator)
		assert.Equal(t, "Roles listed", session.Messages[3].Text)
		assert.Equal(t, int64(20), session.Usage.InputTokens)
		assert.Contains(t, provider.inputs[0].System, tenant, "the default system prompt names the tenant")

		// The run is in the admin's usage store, reported in-process.
		records, total, err := adminServer.AIUsageStore().Runs(AIRunQuery{Instance: AdminNATSService})
		require.NoError(t, err)
		require.Equal(t, 1, total)
		rec := records[0]
		assert.Equal(t, AdminNATSService, rec.Instance)
		assert.Equal(t, tenant, rec.AccountID)
		assert.Equal(t, session.ID, rec.SessionID)
		assert.Equal(t, "claude", rec.Credential)
		assert.Equal(t, 2, rec.Turns)
		assert.Equal(t, 1, rec.ToolCalls)
		assert.Equal(t, int64(20), rec.Usage.InputTokens)
		assert.Empty(t, rec.Error)
		// And in the instance's own store, never in admin.db.
		own, err := adminServer.AIInstance().Usage().Records(context.Background(), time.Time{}, 10)
		require.NoError(t, err)
		require.Len(t, own, 1)
		assert.Equal(t, rec.ID, own[0].ID)
	})

	t.Run("a write tool is denied without AllowWrites", func(t *testing.T) {
		provider.script(
			aiFakeStep{calls: []ai.ToolCall{aiCall("w1", "admin_create_user", `{"body":{"email":"new@tenant.local","password":"pw","roles":["user"]}}`)}},
			aiFakeStep{text: "I was not allowed to do that"},
		)
		rr := do(tenantAdmin, http.MethodPost, "/api/v1/ai/sessions/"+session.ID+"/messages", `{"content":"create a user"}`)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		events := parseAISSE(t, rr.Body.String())
		assert.Equal(t, []string{"usage", "tool_use", "tool_result", "text", "usage", "done"}, aiEventTypes(events))
		require.NotNil(t, events[2].Result)
		assert.True(t, events[2].Result.Denied)
		assert.True(t, events[2].Result.Result.IsError)
		_, _, err := adminServer.GetUserIDByEmail("new@tenant.local")
		assert.Error(t, err, "the denied call must not have reached the handler")
	})

	t.Run("sessions are private to their owner", func(t *testing.T) {
		rr := do(member, http.MethodGet, "/api/v1/ai/sessions/"+session.ID, "")
		assert.Equal(t, http.StatusNotFound, rr.Code)
		rr = do(member, http.MethodDelete, "/api/v1/ai/sessions/"+session.ID, "")
		assert.Equal(t, http.StatusNotFound, rr.Code)
		rr = do(member, http.MethodPost, "/api/v1/ai/sessions/"+session.ID+"/messages", `{"content":"hi"}`)
		assert.Equal(t, http.StatusNotFound, rr.Code)
		rr = do(member, http.MethodGet, "/api/v1/ai/sessions", "")
		assert.Equal(t, "[]\n", rr.Body.String())
	})

	t.Run("mcp endpoint serves the same tools with the caller's bearer", func(t *testing.T) {
		srv := httptest.NewServer(adminServer.Srv.Handler)
		defer srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		// MCP clients cannot set the account header, so the account rides
		// in the query string the ID middleware also reads.
		client, err := mcp.ConnectWithOptions(ctx, "admin", srv.URL+MCPPath+"?accountID="+tenant, tenantAdmin.bearer, mcp.ClientOptions{TrustReadOnly: true})
		require.NoError(t, err)
		defer client.Close()
		tools, err := client.Tools(ctx)
		require.NoError(t, err)
		byName := map[string]ai.Tool{}
		for _, tool := range tools {
			byName[tool.Name()] = tool
		}
		roles, ok := byName["admin_admin_list_roles"]
		require.True(t, ok, "tools = %v", len(tools))
		assert.True(t, roles.ReadOnly())
		// Writes are off, so /mcp serves no write tool, only the memory
		// tools the assistant allows either way.
		assert.NotContains(t, byName, "admin_admin_create_user")
		assert.NotContains(t, byName, "admin_admin_api_request")
		assert.Contains(t, byName, "admin_memory")
		for name, tool := range byName {
			if name != "admin_memory" && name != "admin_remember" {
				assert.True(t, tool.ReadOnly(), "write tool %s served over /mcp with writes off", name)
			}
		}
		res, err := roles.Execute(ctx, json.RawMessage(`{}`))
		require.NoError(t, err)
		assert.False(t, res.IsError, res.Content)
		assert.Contains(t, res.Content, user.RoleAdministrator)

		// Without a bearer the route answers 401 before the SDK sees it.
		_, err = mcp.Connect(ctx, "admin", srv.URL+MCPPath+"?accountID="+tenant, "")
		assert.Error(t, err)
	})

	t.Run("delete session", func(t *testing.T) {
		rr := do(tenantAdmin, http.MethodDelete, "/api/v1/ai/sessions/"+session.ID, "")
		assert.Equal(t, http.StatusNoContent, rr.Code)
		rr = do(tenantAdmin, http.MethodGet, "/api/v1/ai/sessions/"+session.ID, "")
		assert.Equal(t, http.StatusNotFound, rr.Code)
	})
}

func TestStartAIRequiresOptions(t *testing.T) {
	adminServer := newTestAdminServer(t, t.TempDir()+"/admin.db")
	t.Cleanup(adminServer.Close)
	assert.Error(t, adminServer.StartAI(AIOptions{}))
	assert.Nil(t, adminServer.AI())
	assert.Nil(t, adminServer.AIStore())
}

// TestStartAILayout checks the assistant's own store layout under
// {Dir}/ai, that an index collection of the earlier layout ({Dir}/ai was
// the index) is moved instead of breaking the start, and that sessions
// and notes an earlier version kept in admin.db move to the instance
// store.
func TestStartAILayout(t *testing.T) {
	adminServer := newTestAdminServer(t, t.TempDir()+"/admin.db")
	t.Cleanup(adminServer.Close)
	tenant := makeAccount(t, adminServer, "tenant")
	adminServer.AddHandlers()

	// A real collection where the earlier layout kept the index.
	dir := t.TempDir()
	embed := func(context.Context, string) ([]float32, error) { return []float32{1, 0}, nil }
	old, err := rag.Open(filepath.Join(dir, AIDirName))
	require.NoError(t, err)
	col, err := old.Collection(tenant, AIDocumentsPurpose, embed)
	require.NoError(t, err)
	require.NoError(t, rag.Add(context.Background(), col, []rag.Document{{ID: "runbook", Content: "restart the queue"}}))
	require.NoError(t, old.Close())
	entries, err := os.ReadDir(filepath.Join(dir, AIDirName))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	legacyName := entries[0].Name()
	legacy := filepath.Join(dir, AIDirName, legacyName)

	base := adminServer.GetBaseStore()
	require.NoError(t, base.PutScoped(tenant, ai.SessionPrefix, "U1:S1", map[string]string{"id": "S1"}))
	require.NoError(t, base.PutScoped(tenant, memory.NotesPrefix, "U1:/memories/a", map[string]string{"path": "/memories/a"}))
	require.NoError(t, base.PutScoped(tenant, "other", "k", map[string]string{"keep": "me"}))

	factory := func(ai.Credential) (ai.Provider, error) { return &aiFakeProvider{}, nil }
	require.NoError(t, adminServer.StartAI(AIOptions{Dir: dir, Providers: map[string]ai.ProviderFactory{ai.ProviderAnthropic: factory}}))
	assert.DirExists(t, filepath.Join(dir, AIDirName, instance.StoreName))
	assert.DirExists(t, filepath.Join(dir, AIDirName, instance.IndexName))
	assert.DirExists(t, filepath.Join(dir, AIDirName, instance.IndexName, legacyName))
	assert.NoDirExists(t, legacy)
	col, err = adminServer.AIInstance().Index().Collection(tenant, AIDocumentsPurpose, embed)
	require.NoError(t, err)
	hits, err := rag.Query(context.Background(), col, "queue", 1, nil)
	require.NoError(t, err)
	require.Len(t, hits, 1, "the moved collection is served")
	assert.Equal(t, "runbook", hits[0].ID)

	st := adminServer.AIStore()
	require.NotNil(t, st)
	var got map[string]string
	require.NoError(t, st.GetScoped(tenant, ai.SessionPrefix, "U1:S1", &got))
	assert.Equal(t, "S1", got["id"])
	require.NoError(t, st.GetScoped(tenant, memory.NotesPrefix, "U1:/memories/a", &got))
	assert.Error(t, base.GetScoped(tenant, ai.SessionPrefix, "U1:S1", &got), "moved out of admin.db")
	assert.Error(t, base.GetScoped(tenant, memory.NotesPrefix, "U1:/memories/a", &got), "moved out of admin.db")
	require.NoError(t, base.GetScoped(tenant, "other", "k", &got), "other records stay")
}

// startTestAI hosts the assistant on a fresh admin server with one tenant,
// a tenant administrator, and the given write policy.
func startTestAI(t *testing.T, allowWrites bool) (*AdminServer, string, tokenTestUser) {
	t.Helper()
	adminServer := newTestAdminServer(t, t.TempDir()+"/admin.db")
	t.Cleanup(adminServer.Close)
	tenant := makeAccount(t, adminServer, "tenant")
	adminServer.AddHandlers()
	tenantAdmin := makeTokenTestUser(t, adminServer, tenant, "admin@tenant.local", []string{user.RoleAdministrator})
	factory := func(ai.Credential) (ai.Provider, error) { return &aiFakeProvider{}, nil }
	require.NoError(t, adminServer.StartAI(AIOptions{
		Dir:         t.TempDir(),
		Providers:   map[string]ai.ProviderFactory{ai.ProviderAnthropic: factory},
		AllowWrites: allowWrites,
	}))
	return adminServer, tenant, tenantAdmin
}

func TestAIToolSetExcludesSensitiveRoutes(t *testing.T) {
	include, err := aiToolFilter(openapiSpec)
	require.NoError(t, err)
	all, err := apitool.Operations(openapiSpec, apitool.Options{Prefix: AIToolPrefix})
	require.NoError(t, err)
	ops, err := apitool.Operations(openapiSpec, apitool.Options{Prefix: AIToolPrefix, Include: include})
	require.NoError(t, err)
	require.Less(t, len(ops), len(all))

	var spec struct {
		Paths map[string]map[string]struct {
			Tags []string `json:"tags"`
		} `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(openapiSpec, &spec))
	denied := []string{
		"/api/v1/servicecredentials/", "/api/v1/login", "/api/v1/logout", "/api/v1/register",
		"/api/v1/oauth/", "/api/v1/oidc/", "/api/v1/invitations/{token}", "/jwt/",
		"/serviceendpoints/", "/api/v1/ai/", "/mcp",
	}
	names := map[string]bool{}
	for _, op := range ops {
		names[op.Name] = true
		for _, prefix := range denied {
			assert.False(t, strings.HasPrefix(op.Path, prefix), "%s %s (%s) must not be a tool", op.Method, op.Path, op.Name)
		}
		assert.NotEqual(t, "/api/v1/users/{userID}/token", op.Path, op.Name)
		for _, tag := range spec.Paths[op.Path][strings.ToLower(op.Method)].Tags {
			assert.NotEqual(t, aiDeniedTag, tag, "%s %s is tagged %s", op.Method, op.Path, tag)
		}
	}
	for _, name := range []string{"admin_get_service_credentials", "admin_login", "admin_logout", "admin_create_user_token",
		"admin_oauth_authorize", "admin_oauth_callback", "admin_get_invitation", "admin_accept_invitation", "admin_noop",
		"admin_bootstrap_service_endpoints", "admin_mcp", "admin_send_ai_message"} {
		assert.False(t, names[name], "%s must not be generated", name)
	}
	for _, name := range []string{"admin_list_roles", "admin_list_invitations", "admin_create_user", "admin_list_service_endpoints"} {
		assert.True(t, names[name], "%s must still be generated", name)
	}

	// Concrete paths, as api_request sends them.
	for _, p := range []string{"/api/v1/servicecredentials/acct/svc", "/api/v1/users/U123/token", "/api/v1/oidc/acme/authorize",
		"/api/v1/oidc/acme/callback", "/api/v1/invitations/abc/accept", "/api/v1/oauth/google/authorize", "/api/v1/login/"} {
		assert.False(t, include(http.MethodGet, p), p)
	}
	for _, p := range []string{"/api/v1/roles", "/api/v1/oidc/connection", "/api/v1/invitations", "/api/v1/users/U123"} {
		assert.True(t, include(http.MethodGet, p), p)
	}

	// The running server: the generated set and api_request agree.
	adminServer, tenant, tenantAdmin := startTestAI(t, true)
	a := adminServer.AIInstance()
	var generated []string
	for _, tool := range a.APITools() {
		generated = append(generated, tool.Name())
	}
	assert.Contains(t, generated, "admin_list_roles")
	assert.NotContains(t, generated, "admin_get_service_credentials")
	ctx := ai.WithCaller(context.Background(), ai.Caller{AccountID: tenant, UserID: tenantAdmin.user.PublicKey, Bearer: tenantAdmin.bearer})
	res, err := a.RequestTool().Execute(ctx, json.RawMessage(`{"method":"GET","path":"/api/v1/servicecredentials/tenant/svc"}`))
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content, "not available")
	res, err = a.RequestTool().Execute(ctx, json.RawMessage(`{"method":"GET","path":"/api/v1/roles"}`))
	require.NoError(t, err)
	assert.False(t, res.IsError, res.Content)
}

func TestMCPEndpointServesWritesOnlyWhenAllowed(t *testing.T) {
	for _, allow := range []bool{false, true} {
		adminServer, tenant, tenantAdmin := startTestAI(t, allow)
		srv := httptest.NewServer(adminServer.Srv.Handler)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		client, err := mcp.Connect(ctx, "admin", srv.URL+MCPPath+"?accountID="+tenant, tenantAdmin.bearer)
		require.NoError(t, err)
		tools, err := client.Tools(ctx)
		require.NoError(t, err)
		names := map[string]bool{}
		for _, tool := range tools {
			names[tool.Name()] = true
		}
		assert.True(t, names["admin_admin_list_roles"], "allowWrites=%v", allow)
		assert.True(t, names["admin_memory"], "allowWrites=%v", allow)
		assert.Equal(t, allow, names["admin_admin_create_user"], "allowWrites=%v", allow)
		assert.Equal(t, allow, names["admin_admin_api_request"], "allowWrites=%v", allow)
		client.Close()
		cancel()
		srv.Close()
	}
}

// remoteMCPServer serves the given tools over MCP.
func remoteMCPServer(t *testing.T, tools ...ai.Tool) string {
	t.Helper()
	srv := httptest.NewServer(mcp.Handler(func(*http.Request) ([]ai.Tool, error) { return tools, nil }))
	t.Cleanup(srv.Close)
	return srv.URL
}

func remoteTool(name string, readOnly bool) ai.Tool {
	return ai.NewTool(name, "remote "+name, nil, readOnly, func(context.Context, json.RawMessage) (ai.Result, error) {
		return ai.Result{Content: "remote " + name}, nil
	})
}

func TestAIToolsRemoteMCPServers(t *testing.T) {
	adminServer, tenant, tenantAdmin := startTestAI(t, false)
	do := func(method, target, body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		adminServer.Srv.Handler.ServeHTTP(rr, tokenRequest(t, tenant, tenantAdmin, method, target, body, nil))
		return rr
	}
	addCred := func(route, name, endpoint string, tags map[string]string) {
		t.Helper()
		meta := map[string]any{"resource_type": ai.ResourceTypeMCPServer, "endpoint": endpoint}
		if tags != nil {
			meta["tags"] = tags
		}
		b, err := json.Marshal(map[string]any{"name": name, "type": "bearer", "token": "remote-secret", "metadata": meta})
		require.NoError(t, err)
		rr := do(http.MethodPost, route, string(b))
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	}

	// Two credentials named "remote": the user's own and the account's.
	addCred("/api/v1/apitokens", "remote", remoteMCPServer(t, remoteTool("alpha", true)), nil)
	addCred("/api/v1/accounts/me/apitokens", "remote", remoteMCPServer(t, remoteTool("beta", true)), nil)
	// A server named "admin" whose tool collides with admin_list_roles.
	addCred("/api/v1/accounts/me/apitokens", "admin", remoteMCPServer(t, remoteTool("list_roles", true), remoteTool("extra", false)), nil)
	// A trusted server whose read-only hint is honoured.
	addCred("/api/v1/accounts/me/apitokens", "trusted", remoteMCPServer(t, remoteTool("look", true)), map[string]string{ai.TagMCPTrustReadOnly: "true"})
	// A server that cannot be reached is skipped.
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	addCred("/api/v1/accounts/me/apitokens", "dead", dead.URL, nil)

	caller := ai.Caller{AccountID: tenant, UserID: tenantAdmin.user.PublicKey, Bearer: tenantAdmin.bearer}
	ctx := ai.WithCaller(context.Background(), caller)
	tools, err := adminServer.AIInstance().Tools(ctx, caller)
	require.NoError(t, err)
	defer instance.CloseTools(tools)

	byName := map[string]ai.Tool{}
	reg := ai.NewRegistry()
	for _, tool := range tools {
		require.NotContains(t, byName, tool.Name(), "duplicate tool name")
		byName[tool.Name()] = tool
		require.NoError(t, reg.Add(tool))
	}
	assert.Contains(t, byName, "remote_alpha", "the user's credential wins")
	assert.NotContains(t, byName, "remote_beta", "the account's same-named credential is skipped")
	assert.Contains(t, byName, "admin_extra")
	roles := byName["admin_list_roles"]
	require.NotNil(t, roles)
	assert.NotContains(t, roles.Description(), "remote", "the API tool keeps its name")
	assert.False(t, byName["remote_alpha"].ReadOnly(), "untrusted read-only hint ignored")
	assert.True(t, byName["trusted_look"].ReadOnly(), "trusted read-only hint honoured")
	for name := range byName {
		assert.False(t, strings.HasPrefix(name, "dead_"), name)
	}
}

func TestAddAIDocumentsTooLarge(t *testing.T) {
	adminServer, tenant, tenantAdmin := startTestAI(t, false)
	body := `{"documents":[{"id":"a","content":"` + strings.Repeat("x", instance.MaxDocumentsBody) + `"}]}`
	r := tokenRequest(t, tenant, tenantAdmin, http.MethodPost, "/api/v1/ai/documents", "", nil)
	r.Body = io.NopCloser(bytes.NewReader([]byte(body)))
	r.ContentLength = int64(len(body))
	rr := httptest.NewRecorder()
	adminServer.Srv.Handler.ServeHTTP(rr, r)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rr.Code, rr.Body.String())
}
