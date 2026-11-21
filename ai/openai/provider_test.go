package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bboxsys/platform/ai"
)

func sse(data ...string) string {
	var b strings.Builder
	for _, d := range data {
		b.WriteString("data: " + d + "\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func delta(d string) string {
	return `{"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":` + d + `,"finish_reason":null}]}`
}

func finish(reason string) string {
	return `{"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"` + reason + `"}]}`
}

const usageChunk = `{"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":285,"completion_tokens":149,"total_tokens":434,"prompt_tokens_details":{"cached_tokens":12}}}`

// toolUseStream is what llama-server sends for a reasoning model that
// calls one tool: reasoning deltas, a little text, then the call with its
// id in the first delta and the arguments spread over the rest.
var toolUseStream = sse(
	delta(`{"role":"assistant","content":null}`),
	delta(`{"reasoning_content":"The user wants"}`),
	delta(`{"reasoning_content":" thing 42."}`),
	delta(`{"content":"Let me "}`),
	delta(`{"content":"check."}`),
	delta(`{"tool_calls":[{"index":0,"id":"dKExz4c5","type":"function","function":{"name":"get_thing","arguments":"{"}}]}`),
	delta(`{"tool_calls":[{"index":0,"function":{"arguments":"\"id\":\"42\""}}]}`),
	delta(`{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]}`),
	finish("tool_calls"),
	usageChunk,
)

// endTurnStream is a plain answer, with Ollama's spelling of reasoning.
var endTurnStream = sse(
	delta(`{"role":"assistant","content":""}`),
	delta(`{"reasoning":"Simple."}`),
	delta(`{"content":"Thing 42 is fine."}`),
	finish("stop"),
	usageChunk,
)

type captured struct {
	mu      sync.Mutex
	headers []http.Header
	paths   []string
	bodies  []map[string]any
}

type reply struct {
	status int
	ctype  string
	body   string
}

func stream(s string) reply { return reply{status: 200, ctype: "text/event-stream", body: s} }

// newServer answers each request with the next reply, in order.
func newServer(t *testing.T, replies ...reply) (*httptest.Server, *captured) {
	t.Helper()
	c := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("request body: %v", err)
			}
		}
		c.mu.Lock()
		n := len(c.bodies)
		c.headers = append(c.headers, r.Header.Clone())
		c.paths = append(c.paths, r.URL.Path)
		c.bodies = append(c.bodies, body)
		c.mu.Unlock()
		if n >= len(replies) {
			http.Error(w, "no more replies", http.StatusInternalServerError)
			return
		}
		rep := replies[n]
		w.Header().Set("Content-Type", rep.ctype)
		w.WriteHeader(rep.status)
		_, _ = io.WriteString(w, rep.body)
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

func testTools() []ai.Tool {
	schema := json.RawMessage(`{"type":"object","properties":{"id":{"type":"string","description":"the id"}},"required":["id"]}`)
	return []ai.Tool{
		ai.NewTool("get_thing", "Fetch a thing by id.", schema, true, nil),
		ai.NewTool("memory", "notes", nil, false, nil),
	}
}

func dig(t *testing.T, v any, path ...string) any {
	t.Helper()
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("path %v: %T is not an object", path, v)
		}
		v = m[p]
	}
	return v
}

func TestTurnToolUse(t *testing.T) {
	srv, cap := newServer(t, stream(toolUseStream))
	cred := ai.Credential{Name: "local", Bearer: "key-1", Endpoint: srv.URL + "/v1/", Model: "qwen", Tags: map[string]string{ai.TagEffort: "xhigh"}}
	p, err := New(cred, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != ai.ProviderOpenAICompat || p.Model() != "qwen" || p.BaseURL() != srv.URL+"/v1" {
		t.Errorf("name/model/base: %s/%s/%s", p.Name(), p.Model(), p.BaseURL())
	}

	var events []ai.Event
	turn, err := p.Turn(context.Background(), ai.TurnInput{
		System:   "You are the admin assistant.",
		Messages: []ai.Message{p.UserMessage("Look up thing 42.")},
		Tools:    testTools(),
	}, func(e ai.Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}

	// The request.
	if cap.paths[0] != "/v1/chat/completions" {
		t.Errorf("path %s", cap.paths[0])
	}
	if got := cap.headers[0].Get("Authorization"); got != "Bearer key-1" {
		t.Errorf("authorization %q", got)
	}
	body := cap.bodies[0]
	if body["model"] != "qwen" || body["stream"] != true || body["reasoning_effort"] != "high" || body["max_tokens"] != float64(DefaultMaxTokens) {
		t.Errorf("request: %v", body)
	}
	if dig(t, body, "stream_options", "include_usage") != true {
		t.Error("include_usage not requested")
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 2 || dig(t, msgs[0], "role") != "system" || dig(t, msgs[0], "content") != "You are the admin assistant." {
		t.Errorf("messages: %v", msgs)
	}
	if dig(t, msgs[1], "role") != "user" || dig(t, msgs[1], "content") != "Look up thing 42." {
		t.Errorf("user message: %v", msgs[1])
	}
	tools := body["tools"].([]any)
	if len(tools) != 2 || dig(t, tools[0], "type") != "function" || dig(t, tools[0], "function", "name") != "get_thing" {
		t.Errorf("tools: %v", tools)
	}
	if dig(t, tools[0], "function", "parameters", "type") != "object" || dig(t, tools[0], "function", "description") != "Fetch a thing by id." {
		t.Errorf("tool schema: %v", tools[0])
	}
	if dig(t, tools[1], "function", "parameters", "type") != "object" {
		t.Errorf("memory tool is a plain function tool with a schema: %v", tools[1])
	}

	// The turn.
	if turn.StopReason != ai.StopReasonToolUse {
		t.Errorf("stop reason %s", turn.StopReason)
	}
	if turn.Text != "Let me check." {
		t.Errorf("text %q", turn.Text)
	}
	if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].ID != "dKExz4c5" || turn.ToolCalls[0].Name != "get_thing" || string(turn.ToolCalls[0].Input) != `{"id":"42"}` {
		t.Errorf("tool calls: %+v", turn.ToolCalls)
	}
	if turn.Usage != (ai.Usage{InputTokens: 285, OutputTokens: 149, CacheReadTokens: 12}) {
		t.Errorf("usage %+v", turn.Usage)
	}
	var stored []map[string]any
	if err := json.Unmarshal(turn.Message.Content, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0]["role"] != "assistant" || stored[0]["content"] != "Let me check." || stored[0]["reasoning_content"] != "The user wants thing 42." {
		t.Errorf("stored message: %v", stored)
	}
	calls := stored[0]["tool_calls"].([]any)
	if len(calls) != 1 || dig(t, calls[0], "id") != "dKExz4c5" || dig(t, calls[0], "function", "arguments") != `{"id":"42"}` {
		t.Errorf("stored tool calls: %v", calls)
	}
	if turn.Message.Role != ai.RoleAssistant || turn.Message.Text != turn.Text || len(turn.Message.ToolCalls) != 1 {
		t.Errorf("message projection: %+v", turn.Message)
	}

	// The events: thinking, then text, in order; nothing else.
	var kinds []string
	var thinking, text string
	for _, e := range events {
		kinds = append(kinds, string(e.Type))
		switch e.Type {
		case ai.EventThinking:
			thinking += e.Text
		case ai.EventText:
			text += e.Text
		}
	}
	if strings.Join(kinds, ",") != "thinking,thinking,text,text" {
		t.Errorf("events %v", kinds)
	}
	if thinking != "The user wants thing 42." || text != "Let me check." {
		t.Errorf("thinking %q text %q", thinking, text)
	}
}

func TestTurnEndTurnWithHistory(t *testing.T) {
	srv, cap := newServer(t, stream(toolUseStream), stream(endTurnStream))
	cred := ai.Credential{Name: "ollama", Endpoint: "", Model: "qwen3:8b"}
	p, err := NewOllama(cred, Options{HTTPClient: srv.Client(), Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != ai.ProviderOllama || p.BaseURL() != ai.OllamaDefaultEndpoint+"/v1" {
		t.Errorf("name/base: %s/%s", p.Name(), p.BaseURL())
	}
	// Point the default at the test server.
	p, err = NewOllama(ai.Credential{Name: "ollama", Endpoint: srv.URL + "/api", Model: "qwen3:8b"}, Options{Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	if p.BaseURL() != srv.URL+"/v1" {
		t.Errorf("base %s", p.BaseURL())
	}

	history := []ai.Message{p.UserMessage("Look up thing 42.")}
	first, err := p.Turn(context.Background(), ai.TurnInput{Messages: history, Tools: testTools()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	history = append(history, first.Message)
	results := []ai.ToolResult{
		{ID: "dKExz4c5", Name: "get_thing", Result: ai.Result{Content: `{"id":"42","ok":true}`}},
		{ID: "x", Name: "other", Denied: true, Result: ai.Result{Content: "denied by policy"}},
		{ID: "y", Name: "empty", Result: ai.Result{Content: ""}},
	}
	history = append(history, p.ToolResultMessage(results))

	var events []ai.Event
	second, err := p.Turn(context.Background(), ai.TurnInput{Messages: history, Tools: testTools(), MaxTokens: 50}, func(e ai.Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	if second.StopReason != "end_turn" || second.Text != "Thing 42 is fine." || len(second.ToolCalls) != 0 {
		t.Errorf("second turn: %+v", second)
	}
	if len(events) != 2 || events[0].Type != ai.EventThinking || events[0].Text != "Simple." || events[1].Type != ai.EventText {
		t.Errorf("events %+v", events)
	}
	var stored []map[string]any
	_ = json.Unmarshal(second.Message.Content, &stored)
	if _, has := stored[0]["tool_calls"]; has || stored[0]["reasoning_content"] != "Simple." {
		t.Errorf("stored: %v", stored)
	}

	// The second request carried the whole history flattened: user,
	// assistant with tool_calls, three tool messages; no system prompt
	// since none was given; the effort and the caller's max_tokens.
	body := cap.bodies[1]
	if body["reasoning_effort"] != "low" || body["max_tokens"] != float64(50) {
		t.Errorf("request: %v", body)
	}
	if _, has := cap.headers[1]["Authorization"]; has {
		t.Error("an empty bearer must not be sent")
	}
	msgs := body["messages"].([]any)
	var roles []string
	for _, m := range msgs {
		roles = append(roles, dig(t, m, "role").(string))
	}
	if strings.Join(roles, ",") != "user,assistant,tool,tool,tool" {
		t.Errorf("roles %v", roles)
	}
	if dig(t, msgs[1], "reasoning_content") != "The user wants thing 42." {
		t.Errorf("assistant message lost its reasoning: %v", msgs[1])
	}
	if dig(t, msgs[2], "tool_call_id") != "dKExz4c5" || dig(t, msgs[2], "content") != `{"id":"42","ok":true}` {
		t.Errorf("tool message: %v", msgs[2])
	}
	if dig(t, msgs[3], "content") != "Error: denied by policy" || dig(t, msgs[4], "content") != "(no output)" {
		t.Errorf("denied/empty tool messages: %v %v", msgs[3], msgs[4])
	}
	if _, has := msgs[2].(map[string]any)["tool_calls"]; has {
		t.Error("tool message carries tool_calls")
	}
}

func TestTurnCutOffAndIDs(t *testing.T) {
	// A call cut off by the output cap, a call without an id, and a server
	// that finishes with "stop" after a complete call.
	cut := sse(
		delta(`{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"get_thing","arguments":"{\"id\":\"4"}}]}`),
		finish("length"),
	)
	noID := sse(
		delta(`{"tool_calls":[{"type":"function","function":{"name":"get_thing","arguments":"{\"id\":\"1\"}"}}]}`),
		delta(`{"tool_calls":[{"type":"function","function":{"name":"memory","arguments":""}}]}`),
		finish("stop"),
	)
	srv, _ := newServer(t, stream(cut), stream(noID))
	p, _ := New(ai.Credential{Endpoint: srv.URL, Model: "m"}, Options{})

	turn, err := p.Turn(context.Background(), ai.TurnInput{Messages: []ai.Message{p.UserMessage("x")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if turn.StopReason != "max_tokens" || len(turn.ToolCalls) != 1 || string(turn.ToolCalls[0].Input) != "{}" {
		t.Errorf("cut-off turn: %+v", turn)
	}
	var stored []map[string]any
	_ = json.Unmarshal(turn.Message.Content, &stored)
	if dig(t, stored[0]["tool_calls"].([]any)[0], "function", "arguments") != "{}" {
		t.Errorf("stored cut-off arguments: %v", stored)
	}

	turn, err = p.Turn(context.Background(), ai.TurnInput{Messages: []ai.Message{p.UserMessage("x")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if turn.StopReason != ai.StopReasonToolUse || len(turn.ToolCalls) != 2 {
		t.Fatalf("no-id turn: %+v", turn)
	}
	if turn.ToolCalls[0].ID != "call_0" || turn.ToolCalls[1].ID != "call_1" || turn.ToolCalls[0].ID == turn.ToolCalls[1].ID {
		t.Errorf("synthesized ids: %+v", turn.ToolCalls)
	}
	if string(turn.ToolCalls[0].Input) != `{"id":"1"}` || string(turn.ToolCalls[1].Input) != "{}" {
		t.Errorf("inputs: %+v", turn.ToolCalls)
	}
}

func TestTurnErrors(t *testing.T) {
	if _, err := New(ai.Credential{Name: "x", Model: "m"}, Options{}); err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Errorf("missing endpoint: %v", err)
	}
	srv, _ := newServer(t,
		reply{status: 401, ctype: "application/json", body: `{"error":{"message":"invalid api key","type":"authentication_error"}}`},
		reply{status: 500, ctype: "text/plain", body: "boom\nmore"},
		stream(sse(`{"error":{"message":"context too long"}}`)),
		reply{status: 200, ctype: "text/event-stream", body: ""},
	)
	p, _ := New(ai.Credential{Endpoint: srv.URL + "/v1", Model: "m", Bearer: "k"}, Options{})
	in := ai.TurnInput{Messages: []ai.Message{p.UserMessage("x")}}
	for _, want := range []string{"401: invalid api key", "500: boom", "context too long", "ended without a message"} {
		_, err := p.Turn(context.Background(), in, nil)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}

	// No model anywhere.
	q, _ := New(ai.Credential{Endpoint: srv.URL}, Options{})
	if _, err := q.Turn(context.Background(), in, nil); err == nil || !strings.Contains(err.Error(), "no model") {
		t.Errorf("no model: %v", err)
	}
	if q.Model() != "" {
		t.Errorf("model %q", q.Model())
	}

	// Factories build the same providers.
	f, err := Factory(Options{})(ai.Credential{Endpoint: srv.URL, Model: "m"})
	if err != nil || f.Name() != ai.ProviderOpenAICompat {
		t.Errorf("factory: %v %v", f, err)
	}
	o, err := OllamaFactory(Options{})(ai.Credential{Model: "m"})
	if err != nil || o.Name() != ai.ProviderOllama {
		t.Errorf("ollama factory: %v %v", o, err)
	}
}

func TestTurnContextCancelled(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "data: "+delta(`{"content":"partial"}`)+"\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-release
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	p, _ := New(ai.Credential{Endpoint: srv.URL, Model: "m"}, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	_, err := p.Turn(ctx, ai.TurnInput{Messages: []ai.Message{p.UserMessage("x")}}, nil)
	if err == nil || ctx.Err() == nil {
		t.Fatalf("expected a cancellation error, got %v", err)
	}
}

func TestEffortAndLimits(t *testing.T) {
	cred := ai.Credential{Endpoint: "http://x/v1", Model: "m", Tags: map[string]string{ai.TagEffort: "max", ai.TagMaxOutput: "1234", ai.TagContextWindow: "8000"}}
	p, _ := New(cred, Options{})
	if p.effort != "high" || p.maxTokens != 1234 || p.window != 8000 {
		t.Errorf("from tags: %+v", p)
	}
	p, _ = New(cred, Options{Effort: "medium", MaxTokens: 10})
	if p.effort != "medium" || p.maxTokens != 10 {
		t.Errorf("from options: %+v", p)
	}
	p, _ = New(ai.Credential{Endpoint: "http://x", Model: "m", Tags: map[string]string{ai.TagEffort: "silly"}}, Options{})
	if p.effort != "" || p.maxTokens != DefaultMaxTokens {
		t.Errorf("defaults: %+v", p)
	}
}

const modelsBody = `{"object":"list","data":[{"id":"qwen3.6-35b-a3b","object":"model","owned_by":"llamacpp","meta":{"n_ctx":32768,"n_ctx_train":262144}},{"id":"other:latest","object":"model"}]}`

func TestCheck(t *testing.T) {
	srv, cap := newServer(t,
		reply{status: 200, ctype: "application/json", body: modelsBody},
		reply{status: 200, ctype: "application/json", body: modelsBody},
		reply{status: 200, ctype: "application/json", body: modelsBody},
		reply{status: 200, ctype: "application/json", body: modelsBody},
		reply{status: 401, ctype: "application/json", body: `{"error":{"message":"bad key"}}`},
		reply{status: 404, ctype: "text/plain", body: "not here"},
	)
	p, _ := New(ai.Credential{Endpoint: srv.URL + "/v1", Model: "qwen3.6-35b-a3b", Bearer: "k"}, Options{})
	res, err := p.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "qwen3.6-35b-a3b" || res.ContextWindow != 32768 {
		t.Errorf("result %+v", res)
	}
	if cap.paths[0] != "/v1/models" || cap.headers[0].Get("Authorization") != "Bearer k" {
		t.Errorf("request %s %v", cap.paths[0], cap.headers[0])
	}
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "other:latest" || models[1].ID != "qwen3.6-35b-a3b" || models[1].ContextWindow != 32768 || models[0].ContextWindow != 0 {
		t.Errorf("models %+v", models)
	}

	p, _ = New(ai.Credential{Endpoint: srv.URL + "/v1", Model: "missing"}, Options{})
	_, err = p.Check(context.Background())
	var ce *ai.CheckError
	if !asCheck(err, &ce) || ce.Kind != ai.CheckNotFound || !strings.Contains(ce.Message, "offers: other:latest, qwen3.6-35b-a3b") {
		t.Errorf("not found: %v", err)
	}

	p, _ = New(ai.Credential{Endpoint: srv.URL + "/v1"}, Options{})
	_, err = p.Check(context.Background())
	if !asCheck(err, &ce) || ce.Kind != ai.CheckNotFound || !strings.Contains(ce.Message, "no model is set; the server offers") {
		t.Errorf("no model: %v", err)
	}

	p, _ = New(ai.Credential{Endpoint: srv.URL + "/v1", Model: "m"}, Options{})
	_, err = p.Check(context.Background())
	if !asCheck(err, &ce) || ce.Kind != ai.CheckAuth || !strings.Contains(ce.Message, "bad key") {
		t.Errorf("auth: %v", err)
	}
	_, err = p.Check(context.Background())
	if !asCheck(err, &ce) || ce.Kind != ai.CheckNotFound || !strings.Contains(ce.Message, "no models route") {
		t.Errorf("404: %v", err)
	}

	p, _ = New(ai.Credential{Endpoint: "http://127.0.0.1:1/v1", Model: "m"}, Options{})
	_, err = p.Check(context.Background())
	if !asCheck(err, &ce) || ce.Kind != ai.CheckUnreachable {
		t.Errorf("unreachable: %v", err)
	}
}

func TestCheckOllama(t *testing.T) {
	// Ollama: the listing, then /api/show with the context length and
	// capabilities; the ":latest" suffix is implied.
	show := `{"capabilities":["completion","tools","thinking"],"model_info":{"general.architecture":"qwen3","qwen3.context_length":40960}}`
	noTools := `{"capabilities":["completion"],"model_info":{}}`
	srv, cap := newServer(t,
		reply{status: 200, ctype: "application/json", body: modelsBody},
		reply{status: 200, ctype: "application/json", body: show},
		reply{status: 200, ctype: "application/json", body: modelsBody},
		reply{status: 200, ctype: "application/json", body: noTools},
		reply{status: 200, ctype: "application/json", body: modelsBody},
		reply{status: 404, ctype: "text/plain", body: "File Not Found"},
	)
	p, _ := NewOllama(ai.Credential{Endpoint: srv.URL, Model: "other"}, Options{})
	res, err := p.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "other:latest" || res.ContextWindow != 40960 {
		t.Errorf("result %+v", res)
	}
	if cap.paths[0] != "/v1/models" || cap.paths[1] != "/api/show" || cap.bodies[1]["model"] != "other" {
		t.Errorf("requests %v %v", cap.paths, cap.bodies[1])
	}

	_, err = p.Check(context.Background())
	var ce *ai.CheckError
	if !asCheck(err, &ce) || ce.Kind != ai.CheckRejected || !strings.Contains(ce.Message, "cannot call tools") {
		t.Errorf("no tools: %v", err)
	}

	// llama-server behind an ollama credential: no /api/show, the listing
	// decides and the tag supplies the window the listing lacks.
	p, _ = NewOllama(ai.Credential{Endpoint: srv.URL + "/v1", Model: "other:latest", Tags: map[string]string{ai.TagContextWindow: "4096"}}, Options{})
	res, err = p.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "other:latest" || res.ContextWindow != 4096 {
		t.Errorf("result %+v", res)
	}
}

func asCheck(err error, target **ai.CheckError) bool {
	ce, ok := err.(*ai.CheckError)
	if ok {
		*target = ce
	}
	return ok
}
