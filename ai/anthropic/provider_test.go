package anthropic

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

// sse joins canned events into one text/event-stream body.
func sse(events ...string) string {
	return strings.Join(events, "\n\n") + "\n\n"
}

func ev(name, data string) string { return "event: " + name + "\ndata: " + data }

// toolUseStream is a turn with a thinking block, two text deltas, and a
// tool_use whose input arrives in two json deltas.
var toolUseStream = sse(
	ev("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":25,"output_tokens":1,"cache_read_input_tokens":3,"cache_creation_input_tokens":4}}}`),
	ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`),
	ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Considering."}}`),
	ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig123"}}`),
	ev("content_block_stop", `{"type":"content_block_stop","index":0}`),
	ev("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
	ev("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Let me "}}`),
	ev("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"check."}}`),
	ev("content_block_stop", `{"type":"content_block_stop","index":1}`),
	ev("content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_thing","input":{}}}`),
	ev("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"id\":"}}`),
	ev("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"42\"}"}}`),
	ev("content_block_stop", `{"type":"content_block_stop","index":2}`),
	ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":7}}`),
	ev("message_stop", `{"type":"message_stop"}`),
)

// endTurnStream is a plain text answer that also carries a compaction block.
var endTurnStream = sse(
	ev("message_start", `{"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":50,"output_tokens":1}}}`),
	ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"compaction","content":"","encrypted_content":""}}`),
	ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"compaction_delta","content":"Earlier: the user asked about thing 42.","encrypted_content":"enc"}}`),
	ev("content_block_stop", `{"type":"content_block_stop","index":0}`),
	ev("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
	ev("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Thing 42 is fine."}}`),
	ev("content_block_stop", `{"type":"content_block_stop","index":1}`),
	ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":9}}`),
	ev("message_stop", `{"type":"message_stop"}`),
)

type captured struct {
	mu      sync.Mutex
	headers []http.Header
	bodies  []map[string]any
}

// newServer replies with one canned stream per request, in order.
func newServer(t *testing.T, streams ...string) (*httptest.Server, *captured) {
	t.Helper()
	c := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body: %v", err)
		}
		c.mu.Lock()
		n := len(c.bodies)
		c.headers = append(c.headers, r.Header.Clone())
		c.bodies = append(c.bodies, body)
		c.mu.Unlock()
		if n >= len(streams) {
			http.Error(w, "no more streams", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, streams[n])
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

func testTools() []ai.Tool {
	schema := json.RawMessage(`{"type":"object","properties":{"id":{"type":"string","description":"the id"}},"required":["id"],"additionalProperties":false}`)
	get := ai.NewTool("get_thing", "Fetch a thing by id.", schema, true, nil)
	memory := ai.NewTool("memory", "notes", nil, false, nil)
	return []ai.Tool{get, memory}
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
	srv, cap := newServer(t, toolUseStream)
	cred := ai.Credential{Bearer: "key-1", Endpoint: srv.URL, Tags: map[string]string{ai.TagEffort: "high"}}
	p, err := New(cred, Options{Fallbacks: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "anthropic" || p.Model() != DefaultModel {
		t.Errorf("name/model: %s/%s", p.Name(), p.Model())
	}

	var events []ai.Event
	turn, err := p.Turn(context.Background(), ai.TurnInput{
		System:   "You are the admin assistant.",
		Messages: []ai.Message{p.UserMessage("look at thing 42")},
		Tools:    testTools(),
	}, func(e ai.Event) { events = append(events, e) })
	if err != nil {
		t.Fatalf("turn: %v", err)
	}

	// Request shape.
	if len(cap.bodies) != 1 {
		t.Fatalf("requests: %d", len(cap.bodies))
	}
	body := cap.bodies[0]
	hdr := cap.headers[0]
	if hdr.Get("x-api-key") != "key-1" {
		t.Errorf("api key header: %q", hdr.Get("x-api-key"))
	}
	betas := hdr.Get("anthropic-beta")
	for _, want := range []string{"compact-2026-01-12", "server-side-fallback-2026-07-01"} {
		if !strings.Contains(betas, want) {
			t.Errorf("anthropic-beta %q lacks %s", betas, want)
		}
	}
	if body["model"] != "claude-opus-5-5" || body["max_tokens"] != float64(16000) || body["stream"] != true {
		t.Errorf("model/max_tokens/stream: %v %v %v", body["model"], body["max_tokens"], body["stream"])
	}
	system := body["system"].([]any)
	if len(system) != 1 || dig(t, system[0], "text") != "You are the admin assistant." || dig(t, system[0], "cache_control", "type") != "ephemeral" {
		t.Errorf("system: %v", system)
	}
	if dig(t, body, "thinking", "type") != "adaptive" || dig(t, body, "thinking", "display") != "summarized" {
		t.Errorf("thinking: %v", body["thinking"])
	}
	if dig(t, body, "output_config", "effort") != "high" {
		t.Errorf("effort from credential tag: %v", body["output_config"])
	}
	edits := dig(t, body, "context_management", "edits").([]any)
	if len(edits) != 1 || dig(t, edits[0], "type") != "compact_20260112" {
		t.Errorf("context_management: %v", body["context_management"])
	}
	if body["fallbacks"] != "default" {
		t.Errorf("fallbacks: %v", body["fallbacks"])
	}
	for _, forbidden := range []string{"tool_choice"} {
		if _, ok := body[forbidden]; ok {
			t.Errorf("request must not carry %s", forbidden)
		}
	}
	if _, ok := dig(t, body, "thinking").(map[string]any)["budget_tokens"]; ok {
		t.Error("request must not carry budget_tokens")
	}
	tools := body["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools: %v", tools)
	}
	if dig(t, tools[0], "name") != "get_thing" || dig(t, tools[0], "description") != "Fetch a thing by id." {
		t.Errorf("tool 0: %v", tools[0])
	}
	schema := dig(t, tools[0], "input_schema").(map[string]any)
	if schema["type"] != "object" || dig(t, schema, "properties", "id", "description") != "the id" {
		t.Errorf("input_schema: %v", schema)
	}
	if req, _ := schema["required"].([]any); len(req) != 1 || req[0] != "id" {
		t.Errorf("input_schema.required: %v", schema["required"])
	}
	if schema["additionalProperties"] != false {
		t.Errorf("input_schema extra keys were dropped: %v", schema)
	}
	if dig(t, tools[1], "type") != "memory_20250818" || dig(t, tools[1], "name") != "memory" {
		t.Errorf("memory tool: %v", tools[1])
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 1 || dig(t, msgs[0], "role") != "user" {
		t.Fatalf("messages: %v", msgs)
	}
	if blocks := dig(t, msgs[0], "content").([]any); dig(t, blocks[0], "type") != "text" || dig(t, blocks[0], "text") != "look at thing 42" {
		t.Errorf("user content: %v", blocks)
	}

	// Parsed turn.
	if turn.StopReason != ai.StopReasonToolUse {
		t.Errorf("stop reason: %q", turn.StopReason)
	}
	if turn.Text != "Let me check." || turn.Message.Text != turn.Text {
		t.Errorf("text: %q", turn.Text)
	}
	if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].ID != "toolu_1" || turn.ToolCalls[0].Name != "get_thing" || string(turn.ToolCalls[0].Input) != `{"id":"42"}` {
		t.Errorf("tool calls: %+v", turn.ToolCalls)
	}
	want := ai.Usage{InputTokens: 25, OutputTokens: 7, CacheReadTokens: 3, CacheWriteTokens: 4}
	if turn.Usage != want {
		t.Errorf("usage: got %+v, want %+v", turn.Usage, want)
	}
	if turn.Message.Role != ai.RoleAssistant || turn.Message.CreatedAt.IsZero() {
		t.Errorf("message: %+v", turn.Message)
	}
	var content []map[string]any
	if err := json.Unmarshal(turn.Message.Content, &content); err != nil {
		t.Fatalf("message content: %v", err)
	}
	if len(content) != 3 || content[0]["type"] != "thinking" || content[0]["signature"] != "sig123" || content[1]["type"] != "text" || content[2]["type"] != "tool_use" {
		t.Errorf("native content: %v", content)
	}
	if dig(t, content[2], "input", "id") != "42" {
		t.Errorf("tool_use input in native content: %v", content[2])
	}

	// Streamed events: thinking then text deltas, nothing else.
	var texts, thoughts []string
	for _, e := range events {
		switch e.Type {
		case ai.EventText:
			texts = append(texts, e.Text)
		case ai.EventThinking:
			thoughts = append(thoughts, e.Text)
		default:
			t.Errorf("unexpected event %s", e.Type)
		}
	}
	if strings.Join(texts, "") != "Let me check." || strings.Join(thoughts, "") != "Considering." {
		t.Errorf("deltas: text %v thinking %v", texts, thoughts)
	}
}

func TestTurnEndTurnWithHistoryAndCompaction(t *testing.T) {
	srv, cap := newServer(t, toolUseStream, endTurnStream)
	cred := ai.Credential{Bearer: "key-1", Endpoint: srv.URL, Model: "claude-sonnet-5-5", Tags: map[string]string{ai.TagMaxOutput: "4000"}}
	p, err := New(cred, Options{Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	history := []ai.Message{p.UserMessage("look at thing 42")}
	first, err := p.Turn(ctx, ai.TurnInput{Messages: history, Tools: testTools()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	history = append(history, first.Message)
	results := []ai.ToolResult{
		{ID: "toolu_1", Name: "get_thing", Result: ai.Result{Content: strings.Repeat("y", 2500)}},
		{ID: "toolu_x", Name: "write", Denied: true, Result: ai.ErrorResult("tool write was not approved")},
		{ID: "toolu_y", Name: "empty", Result: ai.Result{}},
	}
	rm := p.ToolResultMessage(results)
	if rm.Role != ai.RoleUser || len(rm.ToolResults) != 3 || len(rm.ToolResults[0].Result.Content) > ai.DisplayToolResultBytes+60 {
		t.Errorf("tool result message display copy: %+v", rm.ToolResults)
	}
	history = append(history, rm)

	var events []ai.Event
	second, err := p.Turn(ctx, ai.TurnInput{Messages: history}, func(e ai.Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}

	body := cap.bodies[1]
	if body["model"] != "claude-sonnet-5-5" || body["max_tokens"] != float64(4000) || dig(t, body, "output_config", "effort") != "low" {
		t.Errorf("model/max_tokens/effort: %v %v %v", body["model"], body["max_tokens"], body["output_config"])
	}
	if _, ok := body["fallbacks"]; ok {
		t.Error("fallbacks sent although disabled")
	}
	if _, ok := body["system"]; ok {
		t.Error("empty system must be omitted")
	}
	if _, ok := body["tools"]; ok {
		t.Error("empty tool set must be omitted")
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("history length: %d", len(msgs))
	}
	assistant := dig(t, msgs[1], "content").([]any)
	if len(assistant) != 3 || dig(t, assistant[0], "type") != "thinking" || dig(t, assistant[0], "signature") != "sig123" || dig(t, assistant[2], "id") != "toolu_1" {
		t.Errorf("assistant history replayed wrongly: %v", assistant)
	}
	blocks := dig(t, msgs[2], "content").([]any)
	if len(blocks) != 3 {
		t.Fatalf("tool result blocks: %v", blocks)
	}
	if dig(t, blocks[0], "type") != "tool_result" || dig(t, blocks[0], "tool_use_id") != "toolu_1" || dig(t, blocks[0], "is_error") != false {
		t.Errorf("result block 0: %v", blocks[0])
	}
	if text := dig(t, blocks[0], "content").([]any); len(text) != 1 || len(dig(t, text[0], "text").(string)) != 2500 {
		t.Errorf("result block 0 must carry the full content: %v", text)
	}
	if dig(t, blocks[1], "is_error") != true || dig(t, blocks[1], "tool_use_id") != "toolu_x" {
		t.Errorf("denied result block: %v", blocks[1])
	}
	if text := dig(t, blocks[2], "content").([]any); dig(t, text[0], "text") != "(no output)" {
		t.Errorf("empty result block: %v", blocks[2])
	}

	if second.StopReason != "end_turn" || second.Text != "Thing 42 is fine." || len(second.ToolCalls) != 0 {
		t.Errorf("second turn: %+v", second)
	}
	if second.Usage != (ai.Usage{InputTokens: 50, OutputTokens: 9}) {
		t.Errorf("usage: %+v", second.Usage)
	}
	var compactions int
	for _, e := range events {
		if e.Type == ai.EventCompaction {
			compactions++
			if e.Text != "Earlier: the user asked about thing 42." {
				t.Errorf("compaction summary: %q", e.Text)
			}
		}
	}
	if compactions != 1 {
		t.Errorf("compaction events: %d", compactions)
	}
	// The compaction block is kept in the native content for the next request.
	var content []map[string]any
	_ = json.Unmarshal(second.Message.Content, &content)
	if len(content) != 2 || content[0]["type"] != "compaction" || content[0]["encrypted_content"] != "enc" {
		t.Errorf("native content: %v", content)
	}
}

func TestTurnErrorsAndFactory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"bad model"}}`)
	}))
	t.Cleanup(srv.Close)
	factory := Factory(Options{DisableCompaction: true})
	prov, err := factory(ai.Credential{Bearer: "k", Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prov.Turn(context.Background(), ai.TurnInput{Messages: []ai.Message{prov.UserMessage("x")}}, nil); err == nil || !strings.Contains(err.Error(), "bad model") {
		t.Errorf("api error not surfaced: %v", err)
	}
	if _, err := New(ai.Credential{}, Options{}); err == nil {
		t.Error("missing bearer must fail")
	}
	bad := []ai.Message{{Role: ai.RoleUser, Content: json.RawMessage(`not json`)}}
	if _, err := prov.Turn(context.Background(), ai.TurnInput{Messages: bad}, nil); err == nil {
		t.Error("corrupt history must fail before the request")
	}
}

func TestTurnContextCancelled(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The server only notices a dropped client once the body is read.
		_, _ = io.ReadAll(r.Body)
		once.Do(func() { close(started) })
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	p, _ := New(ai.Credential{Bearer: "k", Endpoint: srv.URL}, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	_, err := p.Turn(ctx, ai.TurnInput{Messages: []ai.Message{p.UserMessage("x")}}, nil)
	if err == nil {
		t.Fatal("cancelled turn must fail")
	}
}

// cutOffToolUseStream is a turn that hits max_tokens in the middle of a
// tool_use block's input JSON; withStop decides whether the block's stop
// event arrives.
func cutOffToolUseStream(withStop bool) string {
	events := []string{
		ev("message_start", `{"type":"message_start","message":{"id":"msg_3","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":1}}}`),
		ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Fetching."}}`),
		ev("content_block_stop", `{"type":"content_block_stop","index":0}`),
		ev("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_9","name":"get_thing","input":{}}}`),
		ev("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"id\":\"4"}}`),
	}
	if withStop {
		events = append(events, ev("content_block_stop", `{"type":"content_block_stop","index":1}`))
	}
	events = append(events,
		ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":16000}}`),
	)
	if withStop {
		events = append(events, ev("message_stop", `{"type":"message_stop"}`))
	}
	return sse(events...)
}

func TestTurnCutOffToolUseInput(t *testing.T) {
	for _, withStop := range []bool{true, false} {
		srv, _ := newServer(t, cutOffToolUseStream(withStop))
		p, _ := New(ai.Credential{Bearer: "k", Endpoint: srv.URL}, Options{})
		turn, err := p.Turn(context.Background(), ai.TurnInput{Messages: []ai.Message{p.UserMessage("x")}, Tools: testTools()}, nil)
		if err != nil {
			t.Fatalf("withStop=%v: a cut-off tool_use must not fail the turn: %v", withStop, err)
		}
		if turn.StopReason != "max_tokens" {
			t.Errorf("withStop=%v: stop reason %q", withStop, turn.StopReason)
		}
		if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].ID != "toolu_9" || string(turn.ToolCalls[0].Input) != "{}" {
			t.Errorf("withStop=%v: tool calls %+v", withStop, turn.ToolCalls)
		}
		var content []map[string]any
		if err := json.Unmarshal(turn.Message.Content, &content); err != nil {
			t.Fatalf("withStop=%v: stored content is not JSON: %v", withStop, err)
		}
		if len(content) != 2 || content[1]["type"] != "tool_use" {
			t.Fatalf("withStop=%v: content %v", withStop, content)
		}
		if in, ok := content[1]["input"].(map[string]any); !ok || len(in) != 0 {
			t.Errorf("withStop=%v: stored tool_use input %v, want {}", withStop, content[1]["input"])
		}
		// The stored message must go back out as valid history.
		if _, err := history([]ai.Message{p.UserMessage("x"), turn.Message}); err != nil {
			t.Errorf("withStop=%v: history: %v", withStop, err)
		}
	}
}

func TestTurnEmptyContentRefusal(t *testing.T) {
	stream := sse(
		ev("message_start", `{"type":"message_start","message":{"id":"msg_4","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":1}}}`),
		ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"refusal","stop_sequence":null},"usage":{"output_tokens":1}}`),
		ev("message_stop", `{"type":"message_stop"}`),
	)
	srv, _ := newServer(t, stream)
	p, _ := New(ai.Credential{Bearer: "k", Endpoint: srv.URL}, Options{})
	turn, err := p.Turn(context.Background(), ai.TurnInput{Messages: []ai.Message{p.UserMessage("x")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if turn.StopReason != "refusal" || turn.Text != "" || turn.Message.Text != "" || len(turn.ToolCalls) != 0 {
		t.Errorf("turn = %+v", turn)
	}
	var content []map[string]any
	if err := json.Unmarshal(turn.Message.Content, &content); err != nil {
		t.Fatal(err)
	}
	if len(content) != 1 || content[0]["type"] != "text" || content[0]["text"] != "(no content: stop reason refusal)" {
		t.Errorf("an empty message must be stored as one text block naming the stop reason: %s", turn.Message.Content)
	}
}
