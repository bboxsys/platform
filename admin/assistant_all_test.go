package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bboxsys/platform/ai"
	"github.com/bboxsys/platform/ai/instance"
	"github.com/bboxsys/platform/ai/instance/natshost"
	"github.com/bboxsys/platform/user"
	"github.com/gorilla/mux"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// routingProvider decides each turn from the tools it is offered, so the
// same factory serves the coordinator and both specialists (the admin's
// assistant and the All assistant resolve the same vault credential, so a
// scripted sequence could not tell their runs apart): offered ask_ tools,
// it asks every specialist once and then summarizes; otherwise it answers
// as the specialist named by its tool prefix.
type routingProvider struct {
	mu        sync.Mutex
	questions []string
}

func (p *routingProvider) Name() string  { return "fake" }
func (p *routingProvider) Model() string { return "fake-1" }
func (p *routingProvider) UserMessage(text string) ai.Message {
	return ai.Message{Role: ai.RoleUser, Content: json.RawMessage(fmt.Sprintf(`{"text":%q}`, text)), Text: text}
}
func (p *routingProvider) ToolResultMessage(results []ai.ToolResult) ai.Message {
	b, _ := json.Marshal(results)
	return ai.Message{Role: ai.RoleUser, Content: b, ToolResults: results}
}

func (p *routingProvider) Turn(_ context.Context, in ai.TurnInput, emit func(ai.Event)) (*ai.Turn, error) {
	var asks []ai.Tool
	specialist := ""
	for _, t := range in.Tools {
		switch {
		case strings.HasPrefix(t.Name(), DelegateToolPrefix):
			asks = append(asks, t)
		case strings.HasPrefix(t.Name(), "things_"):
			specialist = "Things"
		case strings.HasPrefix(t.Name(), AIToolPrefix):
			specialist = "Admin"
		}
	}
	last := in.Messages[len(in.Messages)-1]
	turn := &ai.Turn{StopReason: "end_turn", Usage: ai.Usage{InputTokens: 10, OutputTokens: 5}}
	switch {
	case len(asks) > 0 && last.Text != "":
		// The coordinator's first turn: one question per specialist.
		for i, t := range asks {
			input, _ := json.Marshal(delegateInput{Question: "How many items do you manage? (asked about: " + last.Text + ")"})
			turn.ToolCalls = append(turn.ToolCalls, ai.ToolCall{ID: fmt.Sprintf("c%d", i), Name: t.Name(), Input: input})
		}
		turn.StopReason = ai.StopReasonToolUse
	case len(asks) > 0:
		// The coordinator's second turn: quote what came back.
		var parts []string
		for _, r := range last.ToolResults {
			parts = append(parts, r.Result.Content)
		}
		turn.Text = "Combined: " + strings.Join(parts, " | ")
	default:
		p.mu.Lock()
		p.questions = append(p.questions, specialist+": "+last.Text)
		p.mu.Unlock()
		turn.Text = fmt.Sprintf("%s manages 3 items.", specialist)
	}
	if emit != nil && turn.Text != "" {
		emit(ai.Event{Type: ai.EventText, Text: turn.Text})
	}
	turn.Message = ai.Message{Role: ai.RoleAssistant, Content: json.RawMessage(`{"assistant":true}`), Text: turn.Text, ToolCalls: turn.ToolCalls}
	return turn, nil
}

// TestAssistantGatewayAll hosts the coordinator beside the admin's
// assistant and a NATS-hosted service assistant, and runs one question
// through it: the coordinator asks both specialists as the caller, each on
// a session of its own, and answers from what they returned.
func TestAssistantGatewayAll(t *testing.T) {
	adminServer := newTestAdminServer(t, t.TempDir()+"/admin.db")
	tenant := makeAccount(t, adminServer, "tenant")
	srv := startTestNATS(t)
	g := adminServer.MountAssistantGateway(AssistantGatewayOptions{
		Connect: func(string) (*nats.Conn, func(), error) {
			nc := natsConnect(t, srv)
			return nc, nc.Close, nil
		},
	})
	adminServer.AddHandlers()
	t.Cleanup(adminServer.Close)
	tenantAdmin := makeTokenTestUser(t, adminServer, tenant, "admin@tenant.local", []string{user.RoleAdministrator})
	member := makeTokenTestUser(t, adminServer, tenant, "member@tenant.local", []string{user.RoleUser})

	provider := &routingProvider{}
	factory := func(ai.Credential) (ai.Provider, error) { return provider, nil }
	providers := map[string]ai.ProviderFactory{ai.ProviderAnthropic: factory}

	// No service yet: the admin's assistant alone has nothing to coordinate.
	require.NoError(t, adminServer.StartAI(AIOptions{Dir: t.TempDir(), Providers: providers}))
	require.Nil(t, adminServer.AIAllStore())
	require.Equal(t, []AssistantServiceInfo{{ID: "admin", Name: "Admin"}}, g.Services())
	require.Error(t, g.Register(AssistantService{ID: AllAssistantID}), "the coordinator's id is reserved")

	router := mux.NewRouter()
	router.HandleFunc("/api/v1/things/catalog", func(http.ResponseWriter, *http.Request) {}).Methods(http.MethodGet)
	spec, include, err := instance.RouteTools(instance.RouteToolsOptions{Router: router, Root: "/api/v1/things"})
	require.NoError(t, err)
	auth := NewAuth(AuthConfig{Logger: adminServer.Logger, TrustedAccounts: []string{tenant}})
	host, err := natshost.New(natshost.Options{
		Instance: instance.Options{
			Name: "things_service", Dir: t.TempDir(), ToolPrefix: "things_", OpenAPI: spec, Handler: router, ToolFilter: include,
			Credentials: ai.StaticCredentials{ModelCredential: &ai.Credential{Name: "static", Provider: ai.ProviderAnthropic, Model: "fake-1", Bearer: "k"}},
			Providers:   providers,
		},
		Auth: auth, AdminAccountID: adminServer.AdminAccountID(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { host.Close() })
	require.NoError(t, g.Register(AssistantService{ID: "things", Serve: host.Serve, Stop: host.Stop}))
	require.NotNil(t, adminServer.AIAllStore(), "registering the first service starts the coordinator")
	require.Equal(t, []AssistantServiceInfo{{ID: "admin", Name: "Admin"}, {ID: "things", Name: "Things"}, {ID: "all", Name: "All"}}, g.Services())

	do := func(u tokenTestUser, method, target, body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		r := tokenRequest(t, tenant, u, method, target, body, nil)
		if strings.HasSuffix(target, "/messages") {
			r.Header.Set("Accept", "text/event-stream")
		}
		adminServer.Srv.Handler.ServeHTTP(rr, r)
		return rr
	}
	all := AssistantServicesPath + "/" + AllAssistantID

	rr := do(member, http.MethodGet, all+"/status", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var st ai.Status
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &st))
	assert.False(t, st.Enabled, "no model credential in the vault yet")
	assert.ElementsMatch(t, []string{"ask_admin", "ask_things", "memory"}, st.Tools, "one ask tool per assistant and nothing of the API")
	require.Equal(t, http.StatusNotFound, do(member, http.MethodPost, all+"/documents", `{}`).Code, "only the conversation routes are served")

	body := `{"name":"claude","type":"bearer","token":"sk-test-key","metadata":{"resource_type":"ai_model","tags":{"ai_provider":"anthropic","ai_model":"claude-test"}}}`
	require.Equal(t, http.StatusCreated, do(tenantAdmin, http.MethodPost, "/api/v1/accounts/me/apitokens", body).Code)

	rr = do(member, http.MethodPost, all+"/sessions", `{"title":"everything"}`)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	var session ai.Session
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &session))

	rr = do(member, http.MethodPost, all+"/sessions/"+session.ID+"/messages", `{"content":"How many items are there overall?"}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	events := parseAISSE(t, rr.Body.String())
	types := aiEventTypes(events)
	assert.Contains(t, types, "tool_result")
	assert.NotContains(t, types, "error")
	assert.Equal(t, "done", types[len(types)-1])
	results := map[string]ai.ToolResult{}
	for _, e := range events {
		if e.Type == ai.EventToolResult && e.Result != nil {
			results[e.Result.Name] = *e.Result
		}
	}
	require.Len(t, results, 2)
	for name, specialist := range map[string]string{"ask_things": "Things", "ask_admin": "Admin"} {
		r := results[name]
		assert.False(t, r.Denied, "%s is read-only: the specialist applies its own policy", name)
		assert.False(t, r.Result.IsError, "%s: %s", name, r.Result.Content)
		assert.Contains(t, r.Result.Content, specialist+" manages 3 items.")
		assert.Contains(t, r.Result.Content, "session_id: ")
		var out delegateOutput
		require.NoError(t, json.Unmarshal(r.Result.JSON, &out))
		assert.Equal(t, specialist, out.Assistant)
		assert.NotEmpty(t, out.SessionID)
		assert.Equal(t, "end_turn", out.StopReason)
	}
	var final string
	for _, e := range events {
		if e.Type == ai.EventText {
			final += e.Text
		}
	}
	assert.Contains(t, final, "Combined: ")
	assert.Contains(t, final, "Things manages 3 items.")
	assert.Contains(t, final, "Admin manages 3 items.")

	// Each specialist received the question on a session of the caller's,
	// titled so the user can tell it from their own.
	provider.mu.Lock()
	questions := append([]string(nil), provider.questions...)
	provider.mu.Unlock()
	require.Len(t, questions, 2)
	for _, q := range questions {
		assert.Contains(t, q, "How many items do you manage?")
	}
	things, err := ai.NewStoreSessions(host.Instance().Store()).List(context.Background(), tenant, member.user.PublicKey)
	require.NoError(t, err)
	require.Len(t, things, 1)
	assert.True(t, strings.HasPrefix(things[0].Title, DelegatedTitlePrefix), things[0].Title)
	rr = do(member, http.MethodGet, "/api/v1/ai/sessions", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var adminSessions []ai.SessionSummary
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &adminSessions))
	require.Len(t, adminSessions, 1)
	assert.True(t, strings.HasPrefix(adminSessions[0].Title, DelegatedTitlePrefix), adminSessions[0].Title)

	// The coordinator's own session is in its own store, not the admin's.
	rr = do(member, http.MethodGet, all+"/sessions", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var allSessions []ai.SessionSummary
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &allSessions))
	require.Len(t, allSessions, 1)
	assert.Equal(t, "everything", allSessions[0].Title)

	// Continuing a specialist's session reuses it; a stale id starts a new one.
	tool := newDelegateTool(g.delegateTargets()[1])
	require.Equal(t, "ask_things", tool.Name())
	ctx := ai.WithCaller(context.Background(), ai.Caller{AccountID: tenant, UserID: member.user.PublicKey, Bearer: member.bearer})
	res, err := tool.Execute(ctx, json.RawMessage(fmt.Sprintf(`{"question":"again","session_id":%q}`, things[0].ID)))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	assert.Contains(t, res.Content, "session_id: "+things[0].ID)
	res, err = tool.Execute(ctx, json.RawMessage(`{"question":"fresh","session_id":"gone"}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	assert.NotContains(t, res.Content, "session_id: gone")
	things, err = ai.NewStoreSessions(host.Instance().Store()).List(context.Background(), tenant, member.user.PublicKey)
	require.NoError(t, err)
	assert.Len(t, things, 2)

	// Without a caller identity nothing is asked.
	res, err = tool.Execute(context.Background(), json.RawMessage(`{"question":"x"}`))
	require.NoError(t, err)
	assert.True(t, res.IsError)

	// Unregistering the last service takes All out of the list; the
	// coordinator stays for the next registration.
	g.Unregister("things")
	require.Equal(t, []AssistantServiceInfo{{ID: "admin", Name: "Admin"}}, g.Services())
}

// TestHandlerTransportStreams reads a handler's response while it is
// still writing, and ends with the handler's failure when it panics.
func TestHandlerTransportStreams(t *testing.T) {
	release := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "data: first\n\n")
		http.NewResponseController(w).Flush() //nolint:errcheck
		<-release
		_, _ = io.WriteString(w, "data: second\n\n")
	})
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	resp, err := handlerTransport{h}.RoundTrip(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	buf := make([]byte, 64)
	n, err := resp.Body.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "data: first\n\n", string(buf[:n]), "the first frame arrives before the handler returns")
	close(release)
	rest, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "data: second\n\n", string(rest))

	panicking := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	resp, err = handlerTransport{panicking}.RoundTrip(httptest.NewRequest(http.MethodGet, "/x", nil))
	require.NoError(t, err)
	_, err = io.ReadAll(resp.Body)
	assert.ErrorContains(t, err, "boom")

	// A request cancelled before the handler answers does not hang.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stuck := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { time.Sleep(50 * time.Millisecond) })
	_, err = handlerTransport{stuck}.RoundTrip(httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(ctx))
	assert.ErrorIs(t, err, context.Canceled)
}

// TestReadDelegatedRun collects text, tool names, the stop reason and an
// error from a specialist's event stream, including CRLF frames.
func TestReadDelegatedRun(t *testing.T) {
	stream := "event: text\r\ndata: {\"type\":\"text\",\"text\":\"Hello \"}\r\n\r\n" +
		": ping\n\n" +
		"data: {\"type\":\"tool_use\",\"call\":{\"id\":\"1\",\"name\":\"things_list\",\"input\":{}}}\n\n" +
		"data: {\"type\":\"tool_use\",\"call\":{\"id\":\"2\",\"name\":\"things_list\",\"input\":{}}}\n\n" +
		"data: {\"type\":\"text\",\"text\":\"world\"}\n\n" +
		"data: {\"type\":\"done\",\"stop_reason\":\"max_turns\"}\n\n"
	answer, tools, stop, runErr, err := readDelegatedRun(strings.NewReader(stream))
	require.NoError(t, err)
	assert.Equal(t, "Hello world", answer)
	assert.Equal(t, []string{"things_list"}, tools)
	assert.Equal(t, "max_turns", stop)
	assert.Empty(t, runErr)

	answer, _, _, runErr, err = readDelegatedRun(strings.NewReader("data: {\"type\":\"error\",\"error\":\"model unavailable\"}\n\n"))
	require.NoError(t, err)
	assert.Empty(t, answer)
	assert.Equal(t, "model unavailable", runErr)

	// A stream without a final blank line still yields its last frame.
	answer, _, stop, _, err = readDelegatedRun(strings.NewReader("data: {\"type\":\"done\",\"stop_reason\":\"end_turn\"}\n\ndata: {\"type\":\"text\",\"text\":\"tail\"}"))
	require.NoError(t, err)
	assert.Equal(t, "tail", answer)
	assert.Equal(t, "end_turn", stop)
}
