package instance

import (
	"bytes"
	"context"
	"encoding/json"
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

	admin "github.com/bboxsys/platform/admin/client"
	"github.com/bboxsys/platform/ai"
	"github.com/bboxsys/platform/ai/mcp"
	"github.com/bboxsys/platform/ai/usage"
	"github.com/bboxsys/platform/logger"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testSpec has one read and one write operation, and the instance's own
// status route, which must never become a tool.
const testSpec = `{
  "openapi": "3.0.3",
  "info": {"title": "things", "version": "1"},
  "paths": {
    "/api/v1/things": {
      "get": {"operationId": "listThings", "summary": "List things"},
      "post": {"operationId": "createThing", "summary": "Create a thing",
        "requestBody": {"content": {"application/json": {"schema": {"type": "object"}}}}}
    },
    "/api/v1/secret": {
      "get": {"operationId": "getSecret", "summary": "Never a tool"}
    },
    "/api/v1/ai/status": {
      "get": {"operationId": "aiStatus", "summary": "The assistant itself"}
    }
  }
}`

// fakeProvider answers every turn with "done" and checks keys: "good-key-1"
// passes, anything else fails with an auth error that echoes the key.
type fakeProvider struct{ cred ai.Credential }

func (p *fakeProvider) Name() string  { return "fake" }
func (p *fakeProvider) Model() string { return "fake-1" }
func (p *fakeProvider) UserMessage(text string) ai.Message {
	return ai.Message{Role: ai.RoleUser, Content: json.RawMessage(fmt.Sprintf(`{"text":%q}`, text)), Text: text}
}
func (p *fakeProvider) ToolResultMessage(results []ai.ToolResult) ai.Message {
	b, _ := json.Marshal(results)
	return ai.Message{Role: ai.RoleUser, Content: b}
}
func (p *fakeProvider) Turn(_ context.Context, _ ai.TurnInput, emit func(ai.Event)) (*ai.Turn, error) {
	if emit != nil {
		emit(ai.Event{Type: ai.EventText, Text: "done"})
	}
	return &ai.Turn{
		Text:       "done",
		StopReason: "end_turn",
		Usage:      ai.Usage{InputTokens: 7, OutputTokens: 3},
		Message:    ai.Message{Role: ai.RoleAssistant, Content: json.RawMessage(`{"a":1}`), Text: "done"},
	}, nil
}
func (p *fakeProvider) Check(context.Context) (ai.CheckResult, error) {
	if p.cred.Bearer != "good-key-1" {
		return ai.CheckResult{}, &ai.CheckError{Kind: ai.CheckAuth, Message: "invalid key " + p.cred.Bearer}
	}
	return ai.CheckResult{Model: "fake-1", DisplayName: "Fake One", ContextWindow: 1000}, nil
}

func (p *fakeProvider) ListModels(context.Context) ([]ai.ModelInfo, error) {
	if p.cred.Bearer != "good-key-1" && p.cred.Bearer != "" {
		return nil, &ai.CheckError{Kind: ai.CheckAuth, Message: "invalid key " + p.cred.Bearer}
	}
	if p.cred.Endpoint == "http://empty" {
		return nil, nil
	}
	return []ai.ModelInfo{{ID: "fake-1", ContextWindow: 1000}, {ID: "fake-2"}}, nil
}

func fakeFactory(cred ai.Credential) (ai.Provider, error) { return &fakeProvider{cred: cred}, nil }

// mapCreds resolves model credentials by name; the first is the default.
type mapCreds struct {
	models []ai.Credential
	mcps   []ai.Credential
}

func (m mapCreds) Model(_ context.Context, _, _, name string) (ai.Credential, error) {
	for _, c := range m.models {
		if name == "" || c.Name == name {
			return c, nil
		}
	}
	return ai.Credential{}, ai.ErrNoCredential
}
func (m mapCreds) Models(context.Context, string, string) ([]ai.Credential, error) {
	return m.models, nil
}
func (m mapCreds) Embedding(context.Context, string, string, string) (ai.Credential, error) {
	return ai.Credential{}, ai.ErrNoCredential
}
func (m mapCreds) MCPServers(context.Context, string, string) ([]ai.Credential, error) {
	return m.mcps, nil
}

type fakePublisher struct {
	mu       sync.Mutex
	subjects []string
	records  []usage.Record
}

func (f *fakePublisher) PublishRaw(subject string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subjects = append(f.subjects, subject)
	if strings.HasPrefix(subject, usage.SubjectPrefix+".usage.") {
		var r usage.Record
		if err := json.Unmarshal(data, &r); err != nil {
			return err
		}
		f.records = append(f.records, r)
	}
	return nil
}

func (f *fakePublisher) usageRecords() []usage.Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]usage.Record(nil), f.records...)
}

type fakeLocal struct {
	mu         sync.Mutex
	records    []usage.Record
	heartbeats []usage.Heartbeat
}

func (f *fakeLocal) PutRecord(r usage.Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, r)
	return nil
}

func (f *fakeLocal) PutHeartbeat(h usage.Heartbeat) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heartbeats = append(f.heartbeats, h)
	return nil
}

// thingsHandler serves the spec's routes behind the ID middleware.
func thingsHandler() http.Handler {
	r := mux.NewRouter()
	r.HandleFunc("/api/v1/things", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[{"name":"widget"}]`)
	}).Methods("GET")
	r.HandleFunc("/api/v1/things", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}).Methods("POST")
	return r
}

// harness is an instance mounted on a router with a test bearer wrapper:
// "Bearer tok-{user}" authenticates user in account ACCT.
type harness struct {
	inst   *Instance
	router *mux.Router
	pub    *fakePublisher
	local  *fakeLocal
	dir    string
}

func newHarness(t *testing.T, mutate func(*Options)) *harness {
	t.Helper()
	h := &harness{router: mux.NewRouter(), pub: &fakePublisher{}, local: &fakeLocal{}, dir: t.TempDir()}
	id := admin.NewID(admin.IDConfig{Logger: logger.NewLogger("test", "id")})
	bearer := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !strings.HasPrefix(tok, "tok-") {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			r.Header.Set("X-Blackbox-AccountID", "ACCT")
			r.Header.Set("X-Blackbox-UserID", strings.TrimPrefix(tok, "tok-"))
			id.Middleware(next).ServeHTTP(w, r)
		}
	}
	admins := func(next http.HandlerFunc) http.HandlerFunc {
		return bearer(func(w http.ResponseWriter, r *http.Request) {
			if admin.UserIDFromContext(r.Context()) != "root" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next(w, r)
		})
	}
	o := Options{
		Name:        "things_service",
		Dir:         h.dir,
		Credentials: mapCreds{models: []ai.Credential{{Name: "good", Provider: ai.ProviderAnthropic, Bearer: "good-key-1"}, {Name: "bad", Bearer: "bad-key-12345"}}},
		Providers:   map[string]ai.ProviderFactory{ai.ProviderAnthropic: fakeFactory},
		OpenAPI:     []byte(testSpec),
		Handler:     thingsHandler(),
		ToolPrefix:  "t_",
		ToolFilter:  func(_, path string) bool { return !PathHasPrefix(path, "/api/v1/secret") },
		Publisher:   h.pub,
		Local:       h.local,
		Version:     "v-test",
		Logger:      logger.NewLogger("test", "ai"),
	}
	if mutate != nil {
		mutate(&o)
	}
	inst, err := New(o)
	require.NoError(t, err)
	t.Cleanup(func() { inst.Close() })
	inst.Mount(h.router, "", bearer, admins)
	h.inst = inst
	return h
}

func (h *harness) do(user, method, target, body string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rd)
	if user != "" {
		r.Header.Set("Authorization", "Bearer tok-"+user)
	}
	rr := httptest.NewRecorder()
	h.router.ServeHTTP(rr, r)
	return rr
}

func TestNewValidates(t *testing.T) {
	creds := ai.StaticCredentials{}
	for _, o := range []Options{
		{Dir: t.TempDir(), Credentials: creds},
		{Name: "x", Credentials: creds},
		{Name: "x", Dir: t.TempDir()},
		{Name: "bad name", Dir: t.TempDir(), Credentials: creds},
		{Name: "x", Dir: t.TempDir(), Credentials: creds, OpenAPI: []byte(testSpec)},
	} {
		_, err := New(o)
		assert.Error(t, err, "%+v", o)
	}
}

func TestInstanceOwnStoreAndIndex(t *testing.T) {
	h := newHarness(t, nil)
	assert.DirExists(t, filepath.Join(h.dir, StoreName))
	assert.DirExists(t, filepath.Join(h.dir, IndexName))
	require.NotNil(t, h.inst.Store())
	assert.Equal(t, filepath.Join(h.dir, StoreName), h.inst.Store().Location)
	require.NotNil(t, h.inst.Service())
	opts := h.inst.Usage().Options()
	assert.Equal(t, "things_service", opts.Instance)
	assert.Equal(t, "things_service", opts.Service, "Service defaults to Name")
	assert.Equal(t, usage.DefaultRetention, opts.Retention)
	assert.Equal(t, usage.DefaultHeartbeatInterval, opts.HeartbeatInterval)
	assert.Equal(t, ai.ProviderAnthropic, opts.Provider)

	var names []string
	for _, tool := range h.inst.APITools() {
		names = append(names, tool.Name())
	}
	assert.Contains(t, names, "t_list_things")
	assert.Contains(t, names, "t_create_thing")
	assert.NotContains(t, names, "t_get_secret", "ToolFilter applies")
	assert.NotContains(t, names, "t_ai_status", "the instance's own routes are never tools")
	require.NotNil(t, h.inst.RequestTool())

	// Close is idempotent and releases the store.
	require.NoError(t, h.inst.Close())
	require.NoError(t, h.inst.Close())
}

func TestRunReportsUsage(t *testing.T) {
	h := newHarness(t, nil)
	rr := h.do("alice", http.MethodPost, "/api/v1/ai/sessions", `{"title":"t"}`)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	var session ai.Session
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &session))

	rr = h.do("alice", http.MethodPost, "/api/v1/ai/sessions/"+session.ID+"/messages", `{"content":"hi"}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), `"done"`)

	h.local.mu.Lock()
	require.Len(t, h.local.records, 1)
	rec := h.local.records[0]
	assert.NotEmpty(t, h.local.heartbeats, "Start sends a heartbeat")
	hb := h.local.heartbeats[0]
	h.local.mu.Unlock()
	assert.Equal(t, "things_service", rec.Instance)
	assert.Equal(t, "ACCT", rec.AccountID)
	assert.Equal(t, "alice", rec.UserID)
	assert.Equal(t, session.ID, rec.SessionID)
	assert.Equal(t, "good", rec.Credential)
	assert.Equal(t, 1, rec.Turns)
	assert.Equal(t, int64(7), rec.Usage.InputTokens)
	assert.Equal(t, "v-test", hb.Version)

	require.Eventually(t, func() bool { return len(h.pub.usageRecords()) == 1 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, rec.ID, h.pub.usageRecords()[0].ID)
	h.pub.mu.Lock()
	assert.Contains(t, h.pub.subjects, usage.UsageSubject("things_service"))
	assert.Contains(t, h.pub.subjects, usage.HeartbeatSubject("things_service"))
	h.pub.mu.Unlock()

	own, err := h.inst.Usage().Records(context.Background(), time.Time{}, 10)
	require.NoError(t, err)
	require.Len(t, own, 1, "kept in the instance's own store")
	assert.Equal(t, rec.ID, own[0].ID)
	assert.EqualValues(t, 1, h.inst.Usage().Totals().Runs)
}

func TestTypedNilUsageSinksAreIgnored(t *testing.T) {
	var pub *fakePublisher
	var local *fakeLocal
	h := newHarness(t, func(o *Options) { o.Publisher = pub; o.Local = local })
	assert.Nil(t, h.inst.Usage().Options().Publisher)
	assert.Nil(t, h.inst.Usage().Options().Local)
}

func TestCredentialTestRoute(t *testing.T) {
	h := newHarness(t, nil)
	test := func(name string) CheckResult {
		t.Helper()
		rr := h.do("alice", http.MethodPost, "/api/v1/ai/credentials/test", fmt.Sprintf(`{"name":%q,"kind":"ai_model"}`, name))
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.NotContains(t, rr.Body.String(), "-key-", "the key never reaches the answer")
		var out CheckResult
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
		return out
	}
	out := test("good")
	assert.True(t, out.OK, out.Message)
	assert.Equal(t, "Fake One", out.DisplayName)
	assert.Contains(t, out.Message, "1000 token context")

	out = test("bad")
	assert.False(t, out.OK)
	assert.Equal(t, "auth", out.ErrorKind)
	assert.Equal(t, "invalid key [redacted]", out.Message)

	rr := h.do("alice", http.MethodPost, "/api/v1/ai/credentials/test", `{"name":"missing","kind":"ai_model"}`)
	assert.Equal(t, http.StatusNotFound, rr.Code)
	rr = h.do("alice", http.MethodPost, "/api/v1/ai/credentials/test", `{"name":"good","kind":"bogus"}`)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	rr = h.do("alice", http.MethodPost, "/api/v1/ai/credentials/test", `{"name":"","kind":"ai_model"}`)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	rr = h.do("", http.MethodPost, "/api/v1/ai/credentials/test", `{"name":"good","kind":"ai_model"}`)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestCredentialModelsRoute(t *testing.T) {
	h := newHarness(t, nil)
	list := func(body string) ModelsResult {
		t.Helper()
		rr := h.do("alice", http.MethodPost, "/api/v1/ai/credentials/models", body)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.NotContains(t, rr.Body.String(), "-key-", "the key never reaches the answer")
		var out ModelsResult
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
		return out
	}
	// A typed key.
	out := list(`{"provider":"anthropic","endpoint":"http://x","token":"good-key-1"}`)
	assert.True(t, out.OK, out.Message)
	require.Len(t, out.Models, 2)
	assert.Equal(t, "fake-1", out.Models[0].ID)
	assert.EqualValues(t, 1000, out.Models[0].ContextWindow)

	// A saved credential's key, by name, with no key typed.
	out = list(`{"provider":"anthropic","name":"good"}`)
	assert.True(t, out.OK, out.Message)
	out = list(`{"provider":"anthropic","name":"bad"}`)
	assert.False(t, out.OK)
	assert.Equal(t, "auth", out.ErrorKind)
	assert.Equal(t, "invalid key [redacted]", out.Message)

	// A name that is not saved yet lists without a key; an empty listing
	// is an empty array, not null.
	out = list(`{"provider":"anthropic","name":"new","endpoint":"http://empty"}`)
	assert.True(t, out.OK, out.Message)
	assert.NotNil(t, out.Models)
	assert.Len(t, out.Models, 0)

	rr := h.do("alice", http.MethodPost, "/api/v1/ai/credentials/models", `{"provider":"bogus"}`)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	rr = h.do("alice", http.MethodPost, "/api/v1/ai/credentials/models", `{"endpoint":"http://x"}`)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	rr = h.do("", http.MethodPost, "/api/v1/ai/credentials/models", `{"provider":"anthropic"}`)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestDocumentsRoutes(t *testing.T) {
	h := newHarness(t, nil)
	rr := h.do("root", http.MethodPost, "/api/v1/ai/documents", `{"documents":[{"id":"a","content":"b"}]}`)
	assert.Equal(t, http.StatusPreconditionFailed, rr.Code, "no embedding credential")
	rr = h.do("root", http.MethodGet, "/api/v1/ai/documents/search?q=x", "")
	assert.Equal(t, http.StatusPreconditionFailed, rr.Code, "no embedding credential")
	rr = h.do("alice", http.MethodPost, "/api/v1/ai/documents", `{"documents":[{"id":"a","content":"b"}]}`)
	assert.Equal(t, http.StatusForbidden, rr.Code, "ingestion is wrapped with requireAdmin")

	body := `{"documents":[{"id":"a","content":"` + strings.Repeat("x", MaxDocumentsBody) + `"}]}`
	r := httptest.NewRequest(http.MethodPost, "/api/v1/ai/documents", bytes.NewReader([]byte(body)))
	r.Header.Set("Authorization", "Bearer tok-root")
	rr = httptest.NewRecorder()
	h.router.ServeHTTP(rr, r)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rr.Code)
}

func TestMCPWriteFilter(t *testing.T) {
	for _, allow := range []bool{false, true} {
		h := newHarness(t, func(o *Options) { o.AllowWrites = allow })
		srv := httptest.NewServer(h.router)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		client, err := mcp.Connect(ctx, "srv", srv.URL+MCPPath, "tok-alice")
		require.NoError(t, err)
		tools, err := client.Tools(ctx)
		require.NoError(t, err)
		names := map[string]bool{}
		for _, tool := range tools {
			names[tool.Name()] = true
		}
		assert.True(t, names["srv_t_list_things"], "allowWrites=%v: %v", allow, names)
		assert.True(t, names["srv_memory"], "allowWrites=%v", allow)
		assert.Equal(t, allow, names["srv_t_create_thing"], "allowWrites=%v", allow)
		assert.Equal(t, allow, names["srv_t_api_request"], "allowWrites=%v", allow)
		client.Close()

		_, err = mcp.Connect(ctx, "srv", srv.URL+MCPPath, "")
		assert.Error(t, err, "bearer required")
		cancel()
		srv.Close()
	}
}

func TestRemoteMCPServersInToolSet(t *testing.T) {
	remote := httptest.NewServer(mcp.Handler(func(*http.Request) ([]ai.Tool, error) {
		return []ai.Tool{ai.NewTool("look", "remote look", nil, true, func(context.Context, json.RawMessage) (ai.Result, error) {
			return ai.Result{Content: "seen"}, nil
		})}, nil
	}))
	defer remote.Close()
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	h := newHarness(t, func(o *Options) {
		o.Credentials = ai.StaticCredentials{MCPServerCredentials: []ai.Credential{
			{Name: "ext", Endpoint: remote.URL, Tags: map[string]string{ai.TagMCPTrustReadOnly: "true"}},
			{Name: "ext", Endpoint: dead.URL},
			{Name: "dead", Endpoint: dead.URL},
		}}
	})
	caller := ai.Caller{AccountID: "ACCT", UserID: "alice"}
	tools, err := h.inst.Tools(ai.WithCaller(context.Background(), caller), caller)
	require.NoError(t, err)
	defer CloseTools(tools)
	byName := map[string]ai.Tool{}
	for _, tool := range tools {
		require.NotContains(t, byName, tool.Name())
		byName[tool.Name()] = tool
	}
	require.Contains(t, byName, "ext_look")
	assert.True(t, byName["ext_look"].ReadOnly(), "trusted read-only hint honoured")
	_, closes := byName["ext_look"].(io.Closer)
	assert.True(t, closes)
	for name := range byName {
		assert.False(t, strings.HasPrefix(name, "dead_"), name)
	}
}

func TestStaticCredentialsReportModel(t *testing.T) {
	h := newHarness(t, func(o *Options) {
		o.Credentials = ai.StaticCredentials{ModelCredential: &ai.Credential{Name: "m", Model: "claude-opus-5-5", Bearer: "good-key-1"}}
	})
	opts := h.inst.Usage().Options()
	assert.Equal(t, ai.ProviderAnthropic, opts.Provider)
	assert.Equal(t, "claude-opus-5-5", opts.Model)
}

func TestLegacyIndexIsMoved(t *testing.T) {
	dir := t.TempDir()
	coll := filepath.Join(dir, "abc123")
	require.NoError(t, os.MkdirAll(coll, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(coll, "00000000.gob"), nil, 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "unrelated"), 0o700))
	l := logger.NewLogger("test", "ai")
	assert.Equal(t, 1, migrateLegacyIndex(dir, filepath.Join(dir, IndexName), l))
	assert.FileExists(t, filepath.Join(dir, IndexName, "abc123", "00000000.gob"))
	assert.DirExists(t, filepath.Join(dir, "unrelated"), "only collections move")
	assert.Equal(t, 0, migrateLegacyIndex(dir, filepath.Join(dir, IndexName), l), "only while there is no index")
}

func TestPathHasPrefix(t *testing.T) {
	assert.True(t, PathHasPrefix("/api/v1/users/U1/token", "/api/v1/users/*/token"))
	assert.True(t, PathHasPrefix("/api/v1/users/{userID}/token", "/api/v1/users/*/token"))
	assert.True(t, PathHasPrefix("/api/v1/ai/sessions", "/api/v1/ai"))
	assert.False(t, PathHasPrefix("/api/v1/aim", "/api/v1/ai"))
	assert.True(t, PathMatches("/api/v1/invitations/abc", "/api/v1/invitations/{token}"))
	assert.False(t, PathMatches("/api/v1/invitations/abc/accept", "/api/v1/invitations/{token}"))
}

func TestRedact(t *testing.T) {
	assert.Equal(t, "bad key [redacted] here", Redact("bad key sk-ant-12345678 here", "sk-ant-12345678"))
	assert.Equal(t, "short abc", Redact("short abc", "abc"), "very short secrets are not substituted")
}
