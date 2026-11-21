package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bboxsys/platform/ai"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type ctxKey string

const middlewareKey ctxKey = "middleware"

// testTools returns an echo tool (read-only, structured output), a tool
// that reports IsError, one that returns a Go error, and one that records
// the caller and a context value it sees.
func testTools(seen *sync.Map) []ai.Tool {
	echo := ai.NewTool("echo", "Echo the input", json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`), true,
		func(ctx context.Context, input json.RawMessage) (ai.Result, error) {
			var in struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return ai.ErrorResult("bad input: %v", err), nil
			}
			return ai.Result{Content: "echo: " + in.Text, JSON: json.RawMessage(`{"text":"` + in.Text + `"}`)}, nil
		})
	fails := ai.NewTool("fails", "Always an error result", nil, false,
		func(ctx context.Context, input json.RawMessage) (ai.Result, error) {
			return ai.ErrorResult("it failed"), nil
		})
	goerr := ai.NewTool("goerr", "Returns a Go error", nil, false,
		func(ctx context.Context, input json.RawMessage) (ai.Result, error) {
			return ai.Result{}, errors.New("boom")
		})
	whoami := ai.NewTool("whoami", "Reports the caller", nil, true,
		func(ctx context.Context, input json.RawMessage) (ai.Result, error) {
			c, ok := ai.CallerFromContext(ctx)
			if !ok {
				return ai.ErrorResult("no caller"), nil
			}
			if v, _ := ctx.Value(middlewareKey).(string); v != "" {
				seen.Store("middleware", v)
			}
			seen.Store("caller", c)
			return ai.Result{Content: c.AccountID + "/" + c.UserID}, nil
		})
	return []ai.Tool{echo, fails, goerr, whoami}
}

func TestInMemoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	server := NewServer("test", "0.1", testTools(&sync.Map{}))
	st, ct := sdk.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0.1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := newClient("remote", cs)
	c.trustReadOnly = true
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()

	tools, err := c.Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]ai.Tool{}
	for _, tool := range tools {
		byName[tool.Name()] = tool
		if !ai.ToolNamePattern.MatchString(tool.Name()) {
			t.Errorf("bad name %q", tool.Name())
		}
	}
	echo, ok := byName["remote_echo"]
	if !ok {
		t.Fatalf("tools = %v", byName)
	}
	if !echo.ReadOnly() || byName["remote_fails"].ReadOnly() {
		t.Error("readOnlyHint not mapped")
	}
	if echo.Description() != "Echo the input" {
		t.Errorf("description = %q", echo.Description())
	}
	if !strings.Contains(string(echo.InputSchema()), `"required":["text"]`) {
		t.Errorf("schema = %s", echo.InputSchema())
	}

	res, err := echo.Execute(ctx, json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || res.Content != "echo: hi" {
		t.Errorf("echo = %+v", res)
	}
	if string(res.JSON) != `{"text":"hi"}` {
		t.Errorf("structured content = %s", res.JSON)
	}

	res, err = byName["remote_fails"].Execute(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || res.Content != "it failed" {
		t.Errorf("fails = %+v", res)
	}

	res, err = byName["remote_goerr"].Execute(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "boom") {
		t.Errorf("goerr = %+v", res)
	}
}

func TestStreamableHandler(t *testing.T) {
	var seen sync.Map
	var mu sync.Mutex
	var bearers []string
	h := Handler(func(r *http.Request) ([]ai.Tool, error) {
		mu.Lock()
		bearers = append(bearers, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") == "" {
			return nil, errors.New("unauthenticated")
		}
		return testTools(&seen), nil
	})
	// The route's middleware places the caller in the request context, as
	// auth.Bearer does on the admin server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := ai.WithCaller(r.Context(), ai.Caller{AccountID: "acct", UserID: "user", Bearer: "tok"})
		ctx = context.WithValue(ctx, middlewareKey, "present")
		h.ServeHTTP(w, r.WithContext(ctx))
	}))
	defer srv.Close()

	ctx := context.Background()
	c, err := Connect(ctx, "Admin Server", srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if c.Name() != "Admin_Server" {
		t.Errorf("name = %q", c.Name())
	}
	tools, err := c.Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var whoami ai.Tool
	for _, tool := range tools {
		if tool.Name() == "Admin_Server_whoami" {
			whoami = tool
		}
	}
	if whoami == nil {
		t.Fatalf("whoami missing from %d tools", len(tools))
	}
	res, err := whoami.Execute(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || res.Content != "acct/user" {
		t.Errorf("whoami = %+v", res)
	}
	if v, _ := seen.Load("caller"); v != (ai.Caller{AccountID: "acct", UserID: "user", Bearer: "tok"}) {
		t.Errorf("caller seen by the tool = %+v", v)
	}
	if v, _ := seen.Load("middleware"); v != "present" {
		t.Errorf("request context value did not reach the tool: %v", v)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	if len(bearers) == 0 {
		t.Fatal("getServer never called")
	}
	for _, b := range bearers {
		if b != "Bearer tok" {
			t.Errorf("bearer seen by getServer = %q", b)
		}
	}
	mu.Unlock()

	// Without a bearer getServer fails and the SDK answers 400.
	short, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := Connect(short, "x", srv.URL, ""); err == nil {
		t.Error("expected connect to fail without a bearer")
	}
}

func TestHandlerRejectsGet(t *testing.T) {
	h := Handler(func(r *http.Request) ([]ai.Tool, error) { return nil, nil })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("stateless GET = %d, want 405", rec.Code)
	}
}

func TestNewServerSkipsNonObjectSchema(t *testing.T) {
	bad := ai.NewTool("bad", "", json.RawMessage(`{"type":"string"}`), true, nil)
	good := ai.NewTool("good", "", json.RawMessage(`{"properties":{"a":{"type":"string"}}}`), true, nil)
	server := NewServer("t", "1", []ai.Tool{bad, good})
	ctx := context.Background()
	st, ct := sdk.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "c", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "good" {
		t.Errorf("tools = %+v", res.Tools)
	}
}

func TestStructuredIsAlwaysAnObject(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`:  `{"a":1}`,
		` ["x"] `:  `{"result":["x"]}`,
		`3`:        `{"result":3}`,
		`not json`: `null`,
		``:         `null`,
	}
	for in, want := range cases {
		got, err := json.Marshal(structured(json.RawMessage(in)))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("structured(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestReadOnlyHintTrust(t *testing.T) {
	h := Handler(func(r *http.Request) ([]ai.Tool, error) { return testTools(&sync.Map{}), nil })
	srv := httptest.NewServer(h)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	for _, trust := range []bool{false, true} {
		c, err := ConnectWithOptions(ctx, "remote", srv.URL, "tok", ClientOptions{TrustReadOnly: trust})
		if err != nil {
			t.Fatal(err)
		}
		tools, err := c.Tools(ctx)
		c.Close()
		if err != nil {
			t.Fatal(err)
		}
		ro := map[string]bool{}
		for _, tool := range tools {
			ro[tool.Name()] = tool.ReadOnly()
		}
		if _, ok := ro["remote_echo"]; !ok {
			t.Fatalf("tools = %v", ro)
		}
		if ro["remote_echo"] != trust {
			t.Errorf("trust=%v: echo read-only = %v", trust, ro["remote_echo"])
		}
		if ro["remote_fails"] {
			t.Errorf("trust=%v: a tool without readOnlyHint is read-only", trust)
		}
	}

	// Connect is untrusted.
	c, err := Connect(ctx, "remote", srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	tools, err := c.Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.ReadOnly() {
			t.Errorf("Connect trusted readOnlyHint on %s", tool.Name())
		}
	}
}

func TestConnectHTTPTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	start := time.Now()
	_, err := ConnectWithOptions(context.Background(), "slow", srv.URL, "", ClientOptions{HTTPTimeout: 200 * time.Millisecond})
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("connect took %v", d)
	}
}
