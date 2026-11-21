package ai

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	admin "github.com/bboxsys/platform/admin/client"
	"github.com/bboxsys/platform/logger"
	"github.com/gorilla/mux"
)

// fakeResolver hands out fixed credentials.
type fakeResolver struct {
	mu        sync.Mutex
	model     *Credential
	embedding *Credential
	servers   []Credential
}

func (f *fakeResolver) Model(_ context.Context, _, _, name string) (Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.model == nil || (name != "" && name != f.model.Name) {
		return Credential{}, ErrNoCredential
	}
	return *f.model, nil
}

func (f *fakeResolver) Embedding(_ context.Context, _, _, _ string) (Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.embedding == nil {
		return Credential{}, ErrNoCredential
	}
	return *f.embedding, nil
}

func (f *fakeResolver) MCPServers(context.Context, string, string) ([]Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.servers, nil
}

func (f *fakeResolver) setModel(c *Credential) {
	f.mu.Lock()
	f.model = c
	f.mu.Unlock()
}

// closableTool records that the service closed it after a run.
type closableTool struct {
	Tool
	closed chan struct{}
}

func (c *closableTool) Close() error { close(c.closed); return nil }

// serviceHarness is a mounted Service with the ID middleware standing in
// for the host's bearer wrapper: X-Blackbox-AccountID and X-Blackbox-UserID
// headers become the caller, as they do on a live server after ID+Bearer.
type serviceHarness struct {
	t        *testing.T
	svc      *Service
	router   *mux.Router
	provider *scriptedProvider
	resolver *fakeResolver
	closed   chan struct{}
	preamble string
}

func newServiceHarness(t *testing.T) *serviceHarness {
	t.Helper()
	h := &serviceHarness{t: t, provider: &scriptedProvider{}, resolver: &fakeResolver{}, closed: make(chan struct{}, 8)}
	echo := NewTool("echo", "echo", json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`), true,
		func(ctx context.Context, input json.RawMessage) (Result, error) {
			c, _ := CallerFromContext(ctx)
			return Result{Content: "echo:" + string(input) + ":" + c.UserID + ":" + c.Bearer}, nil
		})
	write := NewTool("write", "write", nil, false, func(context.Context, json.RawMessage) (Result, error) {
		return Result{Content: "wrote"}, nil
	})
	svc, err := NewService(ServiceOptions{
		Credentials: h.resolver,
		Providers: map[string]ProviderFactory{
			ProviderAnthropic: func(cred Credential) (Provider, error) { return h.provider, nil },
		},
		Sessions: NewMemorySessions(),
		Tools: func(ctx context.Context, c Caller) ([]Tool, error) {
			closable := &closableTool{Tool: write, closed: make(chan struct{})}
			go func() { <-closable.closed; h.closed <- struct{}{} }()
			return []Tool{echo, closable}, nil
		},
		Preamble: func(_ context.Context, _ Caller, _ string) string { return h.preamble },
		Logger:   logger.NewLogger("test", "ai"),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.svc = svc
	h.router = mux.NewRouter()
	id := admin.NewID(admin.IDConfig{Logger: logger.NewLogger("test", "id")})
	wrap := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			id.Middleware(next).ServeHTTP(w, r)
		}
	}
	svc.Mount(h.router, "", wrap)
	return h
}

func (h *serviceHarness) request(ctx context.Context, user, method, path, body string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Header.Set("Authorization", "Bearer tok-"+user)
	r.Header.Set("X-Blackbox-AccountID", "ACCT")
	r.Header.Set("X-Blackbox-UserID", user)
	return r.WithContext(ctx)
}

func (h *serviceHarness) do(user, method, path, body string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.router.ServeHTTP(rr, h.request(context.Background(), user, method, path, body))
	return rr
}

// parseSSE decodes every event frame of an SSE body; comment frames are
// counted separately.
func parseSSE(t *testing.T, body string) (events []Event, comments int) {
	t.Helper()
	sc := bufio.NewScanner(strings.NewReader(body))
	var typ string
	var data []string
	flush := func() {
		if len(data) > 0 {
			var e Event
			if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &e); err != nil {
				t.Fatalf("bad frame %q: %v", data, err)
			}
			if string(e.Type) != typ {
				t.Errorf("event line %q does not match data type %q", typ, e.Type)
			}
			events = append(events, e)
		}
		typ, data = "", nil
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, ":"):
			comments++
		case strings.HasPrefix(line, "event: "):
			typ = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = append(data, strings.TrimPrefix(line, "data: "))
		default:
			t.Errorf("unexpected SSE line %q", line)
		}
	}
	flush()
	return events, comments
}

func eventTypes(events []Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, string(e.Type))
	}
	return out
}

func TestServiceRoutes(t *testing.T) {
	h := newServiceHarness(t)

	rr := h.do("alice", http.MethodGet, "/api/v1/ai/status", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rr.Code, rr.Body.String())
	}
	var st Status
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Enabled || st.Model != nil || st.Embedding != nil || strings.Join(st.Tools, ",") != "echo,write" || len(st.MCPServers) != 0 || st.AllowWrites {
		t.Errorf("status without credential = %+v", st)
	}
	if !strings.Contains(rr.Body.String(), `"model":null`) || !strings.Contains(rr.Body.String(), `"models":[]`) {
		t.Errorf("model must serialize as null and models as []: %s", rr.Body.String())
	}
	select {
	case <-h.closed:
	case <-time.After(time.Second):
		t.Error("status did not close the closable tool")
	}

	rr = h.do("alice", http.MethodPost, "/api/v1/ai/sessions", `{"title":"x"}`)
	if rr.Code != http.StatusPreconditionFailed || !strings.Contains(rr.Body.String(), ErrNoModel) {
		t.Fatalf("create without credential = %d %s", rr.Code, rr.Body.String())
	}

	h.resolver.setModel(&Credential{Name: "claude", ResourceType: ResourceTypeModel, Provider: ProviderAnthropic, Model: "claude-test", Bearer: "sk"})
	rr = h.do("alice", http.MethodGet, "/api/v1/ai/status", "")
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || st.Model == nil || st.Model.Name != "claude" || st.Model.Provider != ProviderAnthropic || st.Model.Model != "claude-test" {
		t.Errorf("status with credential = %+v", st)
	}
	if len(st.Models) != 1 || st.Models[0] != *st.Model {
		t.Errorf("a resolver that cannot list reports the default alone: %+v", st.Models)
	}

	rr = h.do("alice", http.MethodPost, "/api/v1/ai/sessions", `{"title":"first","credential":"nope"}`)
	if rr.Code != http.StatusPreconditionFailed {
		t.Fatalf("create with unknown credential = %d", rr.Code)
	}
	rr = h.do("alice", http.MethodPost, "/api/v1/ai/sessions", `{"title":"first"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rr.Code, rr.Body.String())
	}
	var session Session
	if err := json.Unmarshal(rr.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if session.ID == "" || session.AccountID != "ACCT" || session.UserID != "alice" || session.Provider != "fake" || session.Model != "fake-1" || session.Credential != "claude" || session.Messages == nil {
		t.Errorf("session = %+v", session)
	}
	if !strings.Contains(rr.Body.String(), `"messages":[]`) {
		t.Errorf("messages must serialize as an empty array: %s", rr.Body.String())
	}

	rr = h.do("alice", http.MethodGet, "/api/v1/ai/sessions", "")
	var list []SessionSummary
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != session.ID || list[0].Title != "first" {
		t.Errorf("list = %+v", list)
	}
	rr = h.do("bob", http.MethodGet, "/api/v1/ai/sessions", "")
	if rr.Body.String() != "[]\n" {
		t.Errorf("another user's list = %q", rr.Body.String())
	}
	if rr := h.do("bob", http.MethodGet, "/api/v1/ai/sessions/"+session.ID, ""); rr.Code != http.StatusNotFound {
		t.Errorf("another user's get = %d", rr.Code)
	}
	if rr := h.do("bob", http.MethodDelete, "/api/v1/ai/sessions/"+session.ID, ""); rr.Code != http.StatusNotFound {
		t.Errorf("another user's delete = %d", rr.Code)
	}
	if rr := h.do("bob", http.MethodPost, "/api/v1/ai/sessions/"+session.ID+"/messages", `{"content":"hi"}`); rr.Code != http.StatusNotFound {
		t.Errorf("another user's send = %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	r := h.request(context.Background(), "alice", http.MethodGet, "/api/v1/ai/status", "")
	r.Header.Del("Authorization")
	h.router.ServeHTTP(rr, r)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("unwrapped = %d", rr.Code)
	}

	// A run: text, a read-only call that sees the caller, a denied write,
	// then end_turn.
	h.preamble = "Facts recalled from memory:\n- likes terse answers"
	h.provider.steps = []scriptStep{
		{turn: &Turn{Text: "looking", ToolCalls: []ToolCall{call("c1", "echo", `{"text":"hi"}`), call("c2", "write", `{}`)}, StopReason: StopReasonToolUse, Usage: Usage{InputTokens: 10, OutputTokens: 5}}},
		{turn: endTurn("done")},
	}
	rr = h.do("alice", http.MethodPost, "/api/v1/ai/sessions/"+session.ID+"/messages", `{"content":"say hi"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("send = %d %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content type = %q", ct)
	}
	if rr.Header().Get("Cache-Control") != "no-cache" || rr.Header().Get("X-Accel-Buffering") != "no" {
		t.Errorf("headers = %v", rr.Header())
	}
	if !rr.Flushed {
		t.Error("response was never flushed")
	}
	events, _ := parseSSE(t, rr.Body.String())
	got := strings.Join(eventTypes(events), " ")
	want := "text usage tool_use tool_result tool_use tool_result text usage done"
	if got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
	if events[3].Result.Denied || events[3].Result.Result.Content != `echo:{"text":"hi"}:alice:tok-alice` {
		t.Errorf("echo result = %+v", events[3].Result)
	}
	if !events[5].Result.Denied || !events[5].Result.Result.IsError {
		t.Errorf("write must be denied without AllowWrites: %+v", events[5].Result)
	}
	if events[8].StopReason != "end_turn" {
		t.Errorf("done = %+v", events[8])
	}
	select {
	case <-h.closed:
	case <-time.After(time.Second):
		t.Error("run did not close the closable tool")
	}

	rr = h.do("alice", http.MethodGet, "/api/v1/ai/sessions/"+session.ID, "")
	if err := json.Unmarshal(rr.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if len(session.Messages) != 4 {
		t.Fatalf("messages = %d", len(session.Messages))
	}
	if session.Messages[0].Text != "say hi" {
		t.Errorf("display text must be the user's own prompt, got %q", session.Messages[0].Text)
	}
	if !strings.Contains(string(session.Messages[0].Content), "likes terse answers") {
		t.Errorf("the preamble must reach the model: %s", session.Messages[0].Content)
	}
	if len(session.Messages[2].ToolResults) != 2 || session.Messages[3].Text != "done" {
		t.Errorf("history = %+v", session.Messages)
	}
	if session.Usage.InputTokens != 30 || session.Usage.OutputTokens != 7 {
		t.Errorf("usage = %+v", session.Usage)
	}
	if h.provider.inputs[0].System == "" || !strings.Contains(h.provider.inputs[0].System, "ACCT") {
		t.Errorf("system prompt = %q", h.provider.inputs[0].System)
	}

	rr = h.do("alice", http.MethodPost, "/api/v1/ai/sessions/"+session.ID+"/messages", `{"content":"  "}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("empty content = %d", rr.Code)
	}
	rr = h.do("alice", http.MethodDelete, "/api/v1/ai/sessions/"+session.ID, "")
	if rr.Code != http.StatusNoContent {
		t.Errorf("delete = %d", rr.Code)
	}
	if rr := h.do("alice", http.MethodGet, "/api/v1/ai/sessions/"+session.ID, ""); rr.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d", rr.Code)
	}
}

func TestServiceAllowWrites(t *testing.T) {
	h := newServiceHarness(t)
	h.svc.opts.AllowWrites = true
	h.resolver.setModel(&Credential{Name: "claude", Provider: ProviderAnthropic, Bearer: "sk"})
	rr := h.do("alice", http.MethodPost, "/api/v1/ai/sessions", `{}`)
	var session Session
	if err := json.Unmarshal(rr.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	h.provider.steps = []scriptStep{{turn: toolUseTurn(call("c1", "write", `{}`))}, {turn: endTurn("ok")}}
	rr = h.do("alice", http.MethodPost, "/api/v1/ai/sessions/"+session.ID+"/messages", `{"content":"write"}`)
	events, _ := parseSSE(t, rr.Body.String())
	for _, e := range events {
		if e.Type == EventToolResult && (e.Result.Denied || e.Result.Result.Content != "wrote") {
			t.Errorf("write with AllowWrites = %+v", e.Result)
		}
	}
}

func TestServiceConcurrentRunAndDisconnect(t *testing.T) {
	h := newServiceHarness(t)
	h.resolver.setModel(&Credential{Name: "claude", Provider: ProviderAnthropic, Bearer: "sk"})
	rr := h.do("alice", http.MethodPost, "/api/v1/ai/sessions", `{}`)
	var session Session
	if err := json.Unmarshal(rr.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}

	// The first run blocks in the provider until its context is cancelled.
	h.provider.steps = []scriptStep{{blockUntilClose: true}}
	ctx, cancel := context.WithCancel(context.Background())
	first := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.router.ServeHTTP(first, h.request(ctx, "alice", http.MethodPost, "/api/v1/ai/sessions/"+session.ID+"/messages", `{"content":"slow"}`))
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.svc.runsMu.Lock()
		_, running := h.svc.runs[runKey(Caller{AccountID: "ACCT", UserID: "alice"}, session.ID)]
		h.svc.runsMu.Unlock()
		if running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first run never started")
		}
		time.Sleep(5 * time.Millisecond)
	}

	rr = h.do("alice", http.MethodPost, "/api/v1/ai/sessions/"+session.ID+"/messages", `{"content":"again"}`)
	if rr.Code != http.StatusConflict {
		t.Errorf("second run = %d %s", rr.Code, rr.Body.String())
	}

	// The client goes away: the run is cancelled, the handler returns, and
	// the session is persisted with what it had.
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not return after the client disconnected")
	}
	if first.Code != http.StatusOK {
		t.Errorf("stream status = %d", first.Code)
	}
	stored, err := h.svc.opts.Sessions.Get(context.Background(), "ACCT", "alice", session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Messages) != 1 || stored.Messages[0].Text != "slow" {
		t.Errorf("persisted after disconnect = %+v", stored.Messages)
	}
	// The lock is released: a new run works.
	h.provider.steps = []scriptStep{{turn: endTurn("fine")}}
	rr = h.do("alice", http.MethodPost, "/api/v1/ai/sessions/"+session.ID+"/messages", `{"content":"ok"}`)
	events, _ := parseSSE(t, rr.Body.String())
	if rr.Code != http.StatusOK || len(events) == 0 || events[len(events)-1].Type != EventDone {
		t.Errorf("run after disconnect = %d %v", rr.Code, eventTypes(events))
	}
}

func TestServiceProviderCache(t *testing.T) {
	built := 0
	svc, err := NewService(ServiceOptions{
		Credentials: &fakeResolver{},
		Providers: map[string]ProviderFactory{
			ProviderAnthropic: func(cred Credential) (Provider, error) { built++; return &scriptedProvider{}, nil },
		},
		Sessions: NewMemorySessions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	a := Credential{Name: "a", Bearer: "k1"}
	if _, err := svc.provider(a); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.provider(a); err != nil {
		t.Fatal(err)
	}
	if built != 1 {
		t.Errorf("same credential built %d providers", built)
	}
	a.Bearer = "k2"
	if _, err := svc.provider(a); err != nil {
		t.Fatal(err)
	}
	if built != 2 {
		t.Errorf("rotated bearer must build a new provider, built = %d", built)
	}
	if _, err := svc.provider(Credential{Name: "x", Provider: "nope"}); err == nil {
		t.Error("unknown provider must fail")
	}
}

func TestServiceAllowTools(t *testing.T) {
	h := newServiceHarness(t)
	h.svc.opts.AllowTools = []string{"write"}
	rr := h.do("alice", http.MethodPost, "/api/v1/ai/sessions", `{}`)
	var session Session
	if err := json.Unmarshal(rr.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	h.provider.steps = []scriptStep{{turn: toolUseTurn(call("c1", "write", `{}`))}, {turn: endTurn("ok")}}
	rr = h.do("alice", http.MethodPost, "/api/v1/ai/sessions/"+session.ID+"/messages", `{"content":"write"}`)
	events, _ := parseSSE(t, rr.Body.String())
	for _, e := range events {
		if e.Type == EventToolResult && (e.Result.Denied || e.Result.Result.Content != "wrote") {
			t.Errorf("allow-listed write without AllowWrites = %+v", e.Result)
		}
	}
}

// newSession creates a session for alice through the API.
func (h *serviceHarness) newSession() Session {
	h.t.Helper()
	h.resolver.setModel(&Credential{Name: "claude", Provider: ProviderAnthropic, Bearer: "sk"})
	rr := h.do("alice", http.MethodPost, "/api/v1/ai/sessions", `{}`)
	var session Session
	if err := json.Unmarshal(rr.Body.Bytes(), &session); err != nil {
		h.t.Fatal(err)
	}
	return session
}

// runWorks asserts that a fresh run on the session completes, which shows
// its lock was released.
func (h *serviceHarness) runWorks(id string) {
	h.t.Helper()
	h.provider.mu.Lock()
	h.provider.steps = []scriptStep{{turn: endTurn("fine")}}
	h.provider.mu.Unlock()
	rr := h.do("alice", http.MethodPost, "/api/v1/ai/sessions/"+id+"/messages", `{"content":"ok"}`)
	events, _ := parseSSE(h.t, rr.Body.String())
	if rr.Code != http.StatusOK || len(events) == 0 || events[len(events)-1].Type != EventDone {
		h.t.Errorf("run = %d %v", rr.Code, eventTypes(events))
	}
}

func TestServiceRepairsInterruptedRun(t *testing.T) {
	h := newServiceHarness(t)
	session := h.newSession()
	// What a process that died while a tool ran leaves on disk: the
	// assistant's tool_use with no tool result after it.
	stored, err := h.svc.opts.Sessions.Get(context.Background(), "ACCT", "alice", session.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored.Messages = append(stored.Messages,
		h.provider.UserMessage("first"),
		Message{Role: RoleAssistant, Content: json.RawMessage(`{"assistant":true}`), ToolCalls: []ToolCall{call("c1", "echo", `{}`)}},
	)
	if err := h.svc.opts.Sessions.Put(context.Background(), stored); err != nil {
		t.Fatal(err)
	}

	h.provider.steps = []scriptStep{{turn: endTurn("answered")}}
	rr := h.do("alice", http.MethodPost, "/api/v1/ai/sessions/"+session.ID+"/messages", `{"content":"second"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("send = %d %s", rr.Code, rr.Body.String())
	}
	in := h.provider.inputs[0].Messages
	if len(in) != 4 || in[2].Role != RoleUser || len(in[2].ToolResults) != 1 || in[3].Text != "second" {
		t.Fatalf("the model must see the dangling call answered before the new prompt: %+v", in)
	}
	r := in[2].ToolResults[0]
	if r.ID != "c1" || !r.Result.IsError || r.Result.Content != "not run: the previous run was interrupted" {
		t.Errorf("repair result = %+v", r)
	}
	stored, err = h.svc.opts.Sessions.Get(context.Background(), "ACCT", "alice", session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Messages) != 5 || stored.Messages[2].ToolResults[0].ID != "c1" || stored.Messages[4].Text != "answered" {
		t.Errorf("persisted = %+v", stored.Messages)
	}
}

func TestServiceSetupTimeout(t *testing.T) {
	h := newServiceHarness(t)
	session := h.newSession()
	tools := h.svc.opts.Tools
	release := make(chan struct{})
	late := make(chan struct{}, 2)
	// A tool builder that ignores its context, like a stuck remote call.
	h.svc.opts.Tools = func(ctx context.Context, c Caller) ([]Tool, error) {
		<-release
		closable := &closableTool{Tool: echoTool("late", true), closed: make(chan struct{})}
		go func() { <-closable.closed; late <- struct{}{} }()
		return []Tool{closable}, nil
	}
	h.svc.opts.SetupTimeout = 50 * time.Millisecond

	start := time.Now()
	rr := h.do("alice", http.MethodPost, "/api/v1/ai/sessions/"+session.ID+"/messages", `{"content":"hi"}`)
	if rr.Code != http.StatusGatewayTimeout {
		t.Errorf("stuck setup = %d %s", rr.Code, rr.Body.String())
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("setup was not bounded: %v", time.Since(start))
	}

	// Status is bounded the same way and still answers.
	rr = h.do("alice", http.MethodGet, "/api/v1/ai/status", "")
	var st Status
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil || rr.Code != http.StatusOK || !st.Enabled || len(st.Tools) != 0 {
		t.Errorf("status with stuck tools = %d %s", rr.Code, rr.Body.String())
	}

	// Tools that arrive after the deadline are closed, not leaked.
	close(release)
	for i := 0; i < 2; i++ {
		select {
		case <-late:
		case <-time.After(5 * time.Second):
			t.Fatal("tools built after the timeout were not closed")
		}
	}

	// The lock was released.
	h.svc.opts.Tools = tools
	h.runWorks(session.ID)
}

func TestServiceSetupClientDisconnect(t *testing.T) {
	h := newServiceHarness(t)
	session := h.newSession()
	entered := make(chan struct{})
	unblock := make(chan struct{})
	defer close(unblock)
	h.svc.opts.Preamble = func(context.Context, Caller, string) string {
		close(entered)
		<-unblock // ignores its context
		return ""
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.router.ServeHTTP(httptest.NewRecorder(), h.request(ctx, "alice", http.MethodPost, "/api/v1/ai/sessions/"+session.ID+"/messages", `{"content":"hi"}`))
	}()
	<-entered
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a disconnected client must end a stuck setup")
	}
	h.svc.opts.Preamble = nil
	h.runWorks(session.ID)
}

// lockCheckingStore fails a Get that runs while the session is not
// locked by SendMessage.
type lockCheckingStore struct {
	SessionStore
	svc      *Service
	unlocked int
}

func (s *lockCheckingStore) Get(ctx context.Context, accountID, userID, id string) (*Session, error) {
	s.svc.runsMu.Lock()
	_, locked := s.svc.runs[runKey(Caller{AccountID: accountID, UserID: userID}, id)]
	s.svc.runsMu.Unlock()
	if !locked {
		s.unlocked++
	}
	return s.SessionStore.Get(ctx, accountID, userID, id)
}

func TestServiceLocksBeforeLoad(t *testing.T) {
	h := newServiceHarness(t)
	session := h.newSession()
	store := &lockCheckingStore{SessionStore: h.svc.opts.Sessions, svc: h.svc}
	h.svc.opts.Sessions = store
	h.runWorks(session.ID)
	if store.unlocked != 0 {
		t.Errorf("SendMessage loaded the session %d time(s) before locking it", store.unlocked)
	}
}

func TestServiceRunPanic(t *testing.T) {
	h := newServiceHarness(t)
	session := h.newSession()
	h.provider.steps = []scriptStep{{panic: true}}
	rr := h.do("alice", http.MethodPost, "/api/v1/ai/sessions/"+session.ID+"/messages", `{"content":"boom"}`)
	events, _ := parseSSE(t, rr.Body.String())
	if rr.Code != http.StatusOK || len(events) == 0 || events[len(events)-1].Type != EventError {
		t.Fatalf("panicking run = %d %v", rr.Code, eventTypes(events))
	}
	stored, err := h.svc.opts.Sessions.Get(context.Background(), "ACCT", "alice", session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Messages) != 1 || stored.Messages[0].Text != "boom" {
		t.Errorf("persisted after panic = %+v", stored.Messages)
	}
	h.runWorks(session.ID)
}

func TestServiceProviderCacheBounded(t *testing.T) {
	built := 0
	svc, err := NewService(ServiceOptions{
		Credentials: &fakeResolver{},
		Providers: map[string]ProviderFactory{
			ProviderAnthropic: func(cred Credential) (Provider, error) { built++; return &scriptedProvider{}, nil },
		},
		Sessions: NewMemorySessions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxCachedProviders+10; i++ {
		if _, err := svc.provider(Credential{Name: "c", Bearer: "k" + strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if len(svc.providers) != MaxCachedProviders || len(svc.providerKeys) != MaxCachedProviders {
		t.Errorf("cache holds %d providers (%d keys), want %d", len(svc.providers), len(svc.providerKeys), MaxCachedProviders)
	}
	// The newest is still cached; the oldest was evicted and is rebuilt.
	n := built
	svc.provider(Credential{Name: "c", Bearer: "k" + strconv.Itoa(MaxCachedProviders+9)}) //nolint:errcheck
	if built != n {
		t.Error("the newest provider must still be cached")
	}
	svc.provider(Credential{Name: "c", Bearer: "k0"}) //nolint:errcheck
	if built != n+1 {
		t.Error("the oldest provider must have been evicted")
	}
}

// fakeUsage collects the RunRecords a Service reports.
type fakeUsage struct {
	mu   sync.Mutex
	runs []RunRecord
}

func (f *fakeUsage) RecordRun(ctx context.Context, r RunRecord) {
	if _, ok := ctx.Deadline(); !ok {
		panic("usage sink called without a deadline")
	}
	f.mu.Lock()
	f.runs = append(f.runs, r)
	f.mu.Unlock()
}

func (f *fakeUsage) all() []RunRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]RunRecord(nil), f.runs...)
}

func TestServiceRecordsUsage(t *testing.T) {
	h := newServiceHarness(t)
	sink := &fakeUsage{}
	h.svc.opts.Usage = sink
	session := h.newSession()

	run := func(steps ...scriptStep) {
		t.Helper()
		h.provider.mu.Lock()
		h.provider.steps = steps
		h.provider.mu.Unlock()
		rr := h.do("alice", http.MethodPost, "/api/v1/ai/sessions/"+session.ID+"/messages", `{"content":"hi"}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("run = %d %s", rr.Code, rr.Body.String())
		}
	}
	// Two runs with a tool call each: the second must report its own
	// usage, not the session's cumulative.
	for i := 0; i < 2; i++ {
		run(scriptStep{turn: toolUseTurn(call("c1", "echo", `{"text":"x"}`))}, scriptStep{turn: endTurn("done")})
	}
	runs := sink.all()
	if len(runs) != 2 {
		t.Fatalf("records = %d, want 2", len(runs))
	}
	want := Usage{InputTokens: 30, OutputTokens: 7, CacheReadTokens: 1}
	for _, r := range runs {
		if r.AccountID != "ACCT" || r.UserID != "alice" || r.SessionID != session.ID || r.Provider != "fake" || r.Model != "fake-1" || r.Credential != "claude" {
			t.Errorf("identity = %+v", r)
		}
		if r.Turns != 2 || r.ToolCalls != 1 || r.Usage != want || r.StopReason != "end_turn" || r.Error != "" {
			t.Errorf("counts = %+v", r)
		}
		if r.Started.IsZero() || r.Duration <= 0 {
			t.Errorf("timing = %v %v", r.Started, r.Duration)
		}
	}

	// A failing provider records the error, without the bearer in it.
	h.resolver.setModel(&Credential{Name: "claude", Provider: ProviderAnthropic, Bearer: "sk-secret-123"})
	run(scriptStep{err: errors.New("rejected key sk-secret-123")})
	runs = sink.all()
	if len(runs) != 3 {
		t.Fatalf("records = %d, want 3", len(runs))
	}
	last := runs[2]
	if last.Error == "" || strings.Contains(last.Error, "sk-secret-123") || last.Turns != 0 || last.StopReason != "" {
		t.Errorf("error record = %+v", last)
	}

	// Requests that never reach a run are not recorded.
	if rr := h.do("alice", http.MethodPost, "/api/v1/ai/sessions/nope/messages", `{"content":"hi"}`); rr.Code != http.StatusNotFound {
		t.Errorf("unknown session = %d", rr.Code)
	}
	if rr := h.do("alice", http.MethodPost, "/api/v1/ai/sessions/"+session.ID+"/messages", `{"content":""}`); rr.Code != http.StatusBadRequest {
		t.Errorf("empty content = %d", rr.Code)
	}
	h.provider.mu.Lock()
	h.provider.steps = []scriptStep{{blockUntilClose: true}}
	h.provider.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.router.ServeHTTP(httptest.NewRecorder(), h.request(ctx, "alice", http.MethodPost, "/api/v1/ai/sessions/"+session.ID+"/messages", `{"content":"slow"}`))
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.provider.mu.Lock()
		blocked := len(h.provider.steps) == 0
		h.provider.mu.Unlock()
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("blocking run never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if rr := h.do("alice", http.MethodPost, "/api/v1/ai/sessions/"+session.ID+"/messages", `{"content":"again"}`); rr.Code != http.StatusConflict {
		t.Errorf("second run = %d", rr.Code)
	}
	if n := len(sink.all()); n != 3 {
		t.Errorf("records after 404, 400, 409 = %d, want 3", n)
	}
	cancel()
	<-done
	runs = sink.all()
	if len(runs) != 4 || runs[3].Error == "" {
		t.Errorf("cancelled run = %+v", runs[len(runs)-1])
	}

	// No credential: 412, not recorded.
	h.resolver.setModel(nil)
	if rr := h.do("alice", http.MethodPost, "/api/v1/ai/sessions/"+session.ID+"/messages", `{"content":"hi"}`); rr.Code != http.StatusPreconditionFailed {
		t.Errorf("no credential = %d", rr.Code)
	}
	if n := len(sink.all()); n != 4 {
		t.Errorf("records after 412 = %d, want 4", n)
	}
}

func TestServiceRecordsSetupTimeout(t *testing.T) {
	h := newServiceHarness(t)
	sink := &fakeUsage{}
	h.svc.opts.Usage = sink
	session := h.newSession()
	release := make(chan struct{})
	defer close(release)
	h.svc.opts.Tools = func(ctx context.Context, c Caller) ([]Tool, error) {
		<-release
		return nil, nil
	}
	h.svc.opts.SetupTimeout = 30 * time.Millisecond
	if rr := h.do("alice", http.MethodPost, "/api/v1/ai/sessions/"+session.ID+"/messages", `{"content":"hi"}`); rr.Code != http.StatusGatewayTimeout {
		t.Fatalf("setup timeout = %d", rr.Code)
	}
	runs := sink.all()
	if len(runs) != 1 || runs[0].Error != "preparing the run timed out" || runs[0].Turns != 0 {
		t.Errorf("records = %+v", runs)
	}
}

func TestRenameSession(t *testing.T) {
	h := newServiceHarness(t)
	h.resolver.setModel(&Credential{Name: "claude", ResourceType: ResourceTypeModel, Provider: ProviderAnthropic, Model: "claude-test", Bearer: "sk"})
	rr := h.do("alice", http.MethodPost, "/api/v1/ai/sessions", `{"title":"first"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rr.Code, rr.Body.String())
	}
	var created Session
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/ai/sessions/" + created.ID

	rr = h.do("alice", http.MethodPatch, path, `{"title":"  renamed  "}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("rename = %d %s", rr.Code, rr.Body.String())
	}
	var sum SessionSummary
	if err := json.Unmarshal(rr.Body.Bytes(), &sum); err != nil {
		t.Fatal(err)
	}
	if sum.ID != created.ID || sum.Title != "renamed" {
		t.Errorf("summary = %+v", sum)
	}
	rr = h.do("alice", http.MethodGet, "/api/v1/ai/sessions", "")
	if !strings.Contains(rr.Body.String(), `"title":"renamed"`) {
		t.Errorf("list after rename: %s", rr.Body.String())
	}

	// An empty title clears it; a missing or overlong one is rejected.
	rr = h.do("alice", http.MethodPatch, path, `{"title":""}`)
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), `"title"`) {
		t.Errorf("clear = %d %s", rr.Code, rr.Body.String())
	}
	rr = h.do("alice", http.MethodPatch, path, `{}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("missing title = %d", rr.Code)
	}
	rr = h.do("alice", http.MethodPatch, path, `{"title":"`+strings.Repeat("x", MaxTitleRunes+1)+`"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("long title = %d", rr.Code)
	}

	// Another user cannot rename it, and an unknown id is 404.
	rr = h.do("bob", http.MethodPatch, path, `{"title":"theirs"}`)
	if rr.Code != http.StatusNotFound {
		t.Errorf("other user = %d", rr.Code)
	}
	rr = h.do("alice", http.MethodPatch, "/api/v1/ai/sessions/nope", `{"title":"x"}`)
	if rr.Code != http.StatusNotFound {
		t.Errorf("unknown = %d", rr.Code)
	}
}
