package natshost

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	admin "github.com/bboxsys/platform/admin/client"
	"github.com/bboxsys/platform/ai"
	"github.com/bboxsys/platform/ai/instance"
	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/platform/natsapi"
	"github.com/bboxsys/platform/user"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	natsd "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/require"
)

// testProvider calls one read and one write tool on the first turn and
// finishes on the second; with block set it waits for cancellation.
type testProvider struct {
	read, write string
	block       bool
	cancelled   chan struct{}
}

func (p *testProvider) Name() string  { return "test" }
func (p *testProvider) Model() string { return "test-model" }
func (p *testProvider) UserMessage(text string) ai.Message {
	return ai.Message{Role: ai.RoleUser, Text: text}
}
func (p *testProvider) ToolResultMessage(results []ai.ToolResult) ai.Message {
	return ai.Message{Role: ai.RoleUser, ToolResults: results}
}
func (p *testProvider) Turn(ctx context.Context, in ai.TurnInput, emit func(ai.Event)) (*ai.Turn, error) {
	if p.block {
		emit(ai.Event{Type: ai.EventText, Text: "working"})
		<-ctx.Done()
		close(p.cancelled)
		return nil, ctx.Err()
	}
	if len(in.Messages) == 1 {
		calls := []ai.ToolCall{{ID: "r", Name: p.read, Input: json.RawMessage(`{}`)}, {ID: "w", Name: p.write, Input: json.RawMessage(`{"body":{}}`)}}
		return &ai.Turn{StopReason: ai.StopReasonToolUse, ToolCalls: calls, Message: ai.Message{Role: ai.RoleAssistant, ToolCalls: calls}}, nil
	}
	emit(ai.Event{Type: ai.EventText, Text: "finished"})
	return &ai.Turn{StopReason: "end_turn", Text: "finished", Message: ai.Message{Role: ai.RoleAssistant, Text: "finished"}}, nil
}

func testToken(t *testing.T, key nkeys.KeyPair) (string, string) {
	t.Helper()
	account, err := key.PublicKey()
	require.NoError(t, err)
	u, _, err := user.NewUser(&user.UserConfig{Email: uuid.NewString() + "@example.test", Password: "password", AccountID: account}, key)
	require.NoError(t, err)
	token, err := user.NewUserToken(u, 5, key)
	require.NoError(t, err)
	return u.PublicKey, token.String()
}

func testNATS(t *testing.T) *nats.Conn {
	t.Helper()
	s, err := natsd.NewServer(&natsd.Options{Host: "127.0.0.1", Port: -1, NoSigs: true, NoLog: true})
	require.NoError(t, err)
	go s.Start()
	require.True(t, s.ReadyForConnections(5*time.Second))
	t.Cleanup(s.Shutdown)
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

type fixture struct {
	account, owner, token, otherToken string
	auth                              *admin.Auth
	router                            *mux.Router
	provider                          *testProvider
	reads, writes                     atomic.Int32
	opts                              Options
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	key, err := nkeys.CreateAccount()
	require.NoError(t, err)
	f := &fixture{provider: &testProvider{}}
	f.account, _ = key.PublicKey()
	f.owner, f.token = testToken(t, key)
	_, f.otherToken = testToken(t, key)
	log := logger.NewLogger("test", "things")
	f.auth = admin.NewAuth(admin.AuthConfig{Logger: log, TrustedAccounts: []string{f.account}})
	f.router = mux.NewRouter()
	id := admin.NewID(admin.IDConfig{Logger: log})
	f.router.Use(id.Middleware)
	root := "/api/v1/things/objects"
	f.router.HandleFunc(root, f.auth.Bearer(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, f.account, admin.AccountIDFromContext(r.Context()))
		require.Equal(t, f.owner, admin.UserIDFromContext(r.Context()))
		f.reads.Add(1)
		_, _ = io.WriteString(w, `{"objects":[]}`)
	})).Methods("GET")
	f.router.HandleFunc(root, f.auth.Bearer(func(w http.ResponseWriter, r *http.Request) { f.writes.Add(1) })).Methods("POST")
	spec, include, err := instance.RouteTools(instance.RouteToolsOptions{Router: f.router, Root: "/api/v1/things"})
	require.NoError(t, err)
	f.opts = Options{
		Instance: instance.Options{
			Name: "things_service", Dir: t.TempDir(), ToolPrefix: "things_", Logger: log,
			OpenAPI: spec, Handler: f.router, ToolFilter: include,
			Credentials: ai.StaticCredentials{ModelCredential: &ai.Credential{Name: "test", Provider: "test"}},
			Providers:   map[string]ai.ProviderFactory{"test": func(ai.Credential) (ai.Provider, error) { return f.provider, nil }},
		},
		Auth: f.auth, AdminAccountID: f.account,
	}
	return f
}

func TestConversationsOverNATS(t *testing.T) {
	f := newFixture(t)
	h, err := New(f.opts)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, h.Close()) })
	for _, tool := range h.Instance().APITools() {
		if strings.HasPrefix(tool.Description(), "GET ") {
			f.provider.read = tool.Name()
		}
		if strings.HasPrefix(tool.Description(), "POST ") {
			f.provider.write = tool.Name()
		}
	}
	require.NotEmpty(t, f.provider.read)
	require.NotEmpty(t, f.provider.write)
	nc := testNATS(t)
	require.NoError(t, h.Serve(f.account, nc))
	require.Equal(t, []string{f.account}, h.Accounts())
	client := natsapi.NewClient(nc)
	call := func(method, path, body, bearer string) *http.Response {
		req, err := natsapi.NewRequest(context.Background(), method, "things_service", "/api/v1/ai/"+path, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("X-Blackbox-AccountID", f.account)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		req.Header.Set(RequestHeader, uuid.NewString())
		resp, err := client.Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}
	require.Equal(t, http.StatusUnauthorized, call("GET", "status", "", "").StatusCode)
	created := call("POST", "sessions", `{}`, f.token)
	require.Equal(t, http.StatusCreated, created.StatusCode)
	var session ai.Session
	require.NoError(t, json.NewDecoder(created.Body).Decode(&session))
	require.Equal(t, f.owner, session.UserID)
	require.Equal(t, http.StatusNotFound, call("GET", "sessions/"+session.ID, "", f.otherToken).StatusCode, "sessions are per user")
	response := call("POST", "sessions/"+session.ID+"/messages", `{"content":"inspect"}`, f.token)
	require.Equal(t, http.StatusOK, response.StatusCode)
	stream, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Contains(t, string(stream), "finished")
	require.Contains(t, string(stream), `"denied":true`, "writes are denied by default")
	require.Equal(t, int32(1), f.reads.Load())
	require.Zero(t, f.writes.Load())

	// A request cannot switch the tenant pinned to the NATS connection.
	req, _ := natsapi.NewRequest(context.Background(), "GET", "things_service", "/api/v1/ai/status", nil)
	req.Header.Set("X-Blackbox-AccountID", "another-account")
	wrong, err := client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, wrong.StatusCode)
	_ = wrong.Body.Close()

	// Cancellation: only the caller that started a run may cancel it.
	f.provider.block = true
	f.provider.cancelled = make(chan struct{})
	requestID := uuid.NewString()
	req, _ = natsapi.NewRequest(context.Background(), "POST", "things_service", "/api/v1/ai/sessions/"+session.ID+"/messages", strings.NewReader(`{"content":"wait"}`))
	req.Header.Set("X-Blackbox-AccountID", f.account)
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set(RequestHeader, requestID)
	running, err := client.Do(req)
	require.NoError(t, err)
	buf := make([]byte, 1024)
	n, err := running.Body.Read(buf)
	require.NoError(t, err)
	require.Contains(t, string(buf[:n]), "working")
	require.Equal(t, http.StatusNoContent, call("POST", "requests/"+requestID+"/cancel", "", f.otherToken).StatusCode)
	select {
	case <-f.provider.cancelled:
		t.Fatal("another user cancelled this request")
	default:
	}
	require.Equal(t, http.StatusNoContent, call("POST", "requests/"+requestID+"/cancel", "", f.token).StatusCode)
	select {
	case <-f.provider.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not cancel")
	}
	_, err = io.ReadAll(running.Body)
	require.NoError(t, err)
	_ = running.Body.Close()
	require.NoError(t, h.Close())

	// The service's own instance store survives reopening.
	restored, err := New(f.opts)
	require.NoError(t, err)
	defer restored.Close()
	stored, err := ai.NewStoreSessions(restored.Instance().Store()).Get(context.Background(), f.account, f.owner, session.ID)
	require.NoError(t, err)
	require.NotEmpty(t, stored.Messages)
}

func TestRequestIdentityAndEarlyCancel(t *testing.T) {
	f := newFixture(t)
	h, err := New(f.opts)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, h.Close()) })
	do := func(method, path, requestID string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("X-Blackbox-AccountID", f.account)
		r.Header.Set("Authorization", "Bearer "+f.token)
		if requestID != "" {
			r.Header.Set(RequestHeader, requestID)
		}
		w := httptest.NewRecorder()
		h.Handler().ServeHTTP(w, r)
		return w
	}
	require.Equal(t, http.StatusBadRequest, do("GET", "/api/v1/ai/status", "not-a-uuid").Code)
	require.Equal(t, http.StatusOK, do("GET", "/api/v1/ai/status", "").Code, "a request without an id gets one")
	require.Equal(t, http.StatusBadRequest, do("POST", "/api/v1/ai/requests/not-a-uuid/cancel", "").Code)

	// A cancel that arrives first refuses the request when it comes.
	early := uuid.NewString()
	require.Equal(t, http.StatusNoContent, do("POST", "/api/v1/ai/requests/"+early+"/cancel", "").Code)
	require.Equal(t, http.StatusRequestTimeout, do("GET", "/api/v1/ai/status", early).Code)
	require.Equal(t, http.StatusOK, do("GET", "/api/v1/ai/status", early).Code, "an early cancel is consumed once")

	// Stop for an account cancels its runs; Close refuses new requests.
	h.Stop(f.account)
	require.NoError(t, h.Close())
	require.Equal(t, http.StatusServiceUnavailable, do("GET", "/api/v1/ai/status", "").Code)

	_, err = New(Options{Instance: f.opts.Instance})
	require.Error(t, err, "Bearer or Auth is required")
	bad := f.opts
	bad.Instance.Name = "not a service"
	_, err = New(bad)
	require.Error(t, err)
}
