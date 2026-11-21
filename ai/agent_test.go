package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// scriptedProvider replays a fixed list of turns. A nil *Turn with an error
// fails that turn; a blockUntilCancel step waits for the context.
type scriptedProvider struct {
	mu     sync.Mutex
	steps  []scriptStep
	inputs []TurnInput
}

type scriptStep struct {
	turn            *Turn
	err             error
	blockUntilClose bool
	panic           bool
}

func (p *scriptedProvider) Name() string  { return "fake" }
func (p *scriptedProvider) Model() string { return "fake-1" }

func (p *scriptedProvider) UserMessage(text string) Message {
	return Message{Role: RoleUser, Content: json.RawMessage(fmt.Sprintf(`{"text":%q}`, text)), Text: text}
}

func (p *scriptedProvider) ToolResultMessage(results []ToolResult) Message {
	b, _ := json.Marshal(results)
	return Message{Role: RoleUser, Content: b}
}

func (p *scriptedProvider) Turn(ctx context.Context, in TurnInput, emit func(Event)) (*Turn, error) {
	p.mu.Lock()
	p.inputs = append(p.inputs, in)
	if len(p.steps) == 0 {
		p.mu.Unlock()
		return nil, errors.New("script exhausted")
	}
	step := p.steps[0]
	p.steps = p.steps[1:]
	p.mu.Unlock()
	if step.blockUntilClose {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if step.panic {
		panic("provider exploded")
	}
	if step.err != nil {
		return nil, step.err
	}
	if emit != nil && step.turn.Text != "" {
		emit(Event{Type: EventText, Text: step.turn.Text})
	}
	t := *step.turn
	t.Message = Message{Role: RoleAssistant, Content: json.RawMessage(`{"assistant":true}`), Text: t.Text, ToolCalls: t.ToolCalls}
	return &t, nil
}

func toolUseTurn(calls ...ToolCall) *Turn {
	return &Turn{ToolCalls: calls, StopReason: StopReasonToolUse, Usage: Usage{InputTokens: 10, OutputTokens: 5}}
}

func endTurn(text string) *Turn {
	return &Turn{Text: text, StopReason: "end_turn", Usage: Usage{InputTokens: 20, OutputTokens: 2, CacheReadTokens: 1}}
}

func call(id, name, input string) ToolCall {
	return ToolCall{ID: id, Name: name, Input: json.RawMessage(input)}
}

type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recorder) emit(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) types() []EventType {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]EventType, 0, len(r.events))
	for _, e := range r.events {
		out = append(out, e.Type)
	}
	return out
}

func (r *recorder) last() Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events[len(r.events)-1]
}

func echoTool(name string, readOnly bool) Tool {
	return NewTool(name, "echo", nil, readOnly, func(ctx context.Context, input json.RawMessage) (Result, error) {
		return Result{Content: name + ":" + string(input)}, nil
	})
}

func TestNewValidates(t *testing.T) {
	if _, err := New(Options{}); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("nil provider: got %v, want ErrNoProvider", err)
	}
	p := &scriptedProvider{}
	_, err := New(Options{Provider: p, Tools: []Tool{echoTool("a", true), echoTool("a", true)}})
	if !errors.Is(err, ErrDuplicateTool) {
		t.Fatalf("duplicate tool: got %v", err)
	}
	_, err = New(Options{Provider: p, Tools: []Tool{echoTool("bad name", true)}})
	if !errors.Is(err, ErrInvalidToolName) {
		t.Fatalf("invalid tool name: got %v", err)
	}
}

func TestRunMultiTurnToolUse(t *testing.T) {
	p := &scriptedProvider{steps: []scriptStep{
		{turn: toolUseTurn(call("c1", "read", `{"a":1}`))},
		{turn: toolUseTurn(call("c2", "read", `{"a":2}`))},
		{turn: endTurn("done")},
	}}
	agent, err := New(Options{Provider: p, Tools: []Tool{echoTool("read", true)}, System: "sys"})
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{AccountID: "acc", UserID: "u1"}
	rec := &recorder{}
	if err := agent.Run(context.Background(), session, "hello", rec.emit); err != nil {
		t.Fatalf("run: %v", err)
	}

	// user, assistant, results, assistant, results, assistant
	if len(session.Messages) != 6 {
		t.Fatalf("messages: got %d, want 6", len(session.Messages))
	}
	roles := []Role{RoleUser, RoleAssistant, RoleUser, RoleAssistant, RoleUser, RoleAssistant}
	for i, r := range roles {
		if session.Messages[i].Role != r {
			t.Errorf("message %d role: got %s, want %s", i, session.Messages[i].Role, r)
		}
		if session.Messages[i].CreatedAt.IsZero() {
			t.Errorf("message %d has no CreatedAt", i)
		}
	}
	if session.Messages[0].Text != "hello" {
		t.Errorf("user text: %q", session.Messages[0].Text)
	}
	if got := session.Messages[2].ToolResults; len(got) != 1 || got[0].ID != "c1" || got[0].Result.Content != `read:{"a":1}` {
		t.Errorf("tool results on message 2: %+v", got)
	}
	if session.Provider != "fake" || session.Model != "fake-1" {
		t.Errorf("session provider/model: %s/%s", session.Provider, session.Model)
	}
	if session.UpdatedAt.IsZero() {
		t.Error("UpdatedAt not set")
	}
	want := Usage{InputTokens: 40, OutputTokens: 12, CacheReadTokens: 1}
	if session.Usage != want {
		t.Errorf("usage: got %+v, want %+v", session.Usage, want)
	}
	// The whole history goes to every turn, and the session model is passed through.
	if len(p.inputs) != 3 || len(p.inputs[2].Messages) != 5 || p.inputs[2].System != "sys" {
		t.Errorf("turn inputs: %d turns, last had %d messages", len(p.inputs), len(p.inputs[len(p.inputs)-1].Messages))
	}
	if p.inputs[0].Model != "" || p.inputs[1].Model != "fake-1" {
		t.Errorf("model passed to turns: %q then %q", p.inputs[0].Model, p.inputs[1].Model)
	}

	wantTypes := []EventType{
		EventUsage, EventToolUse, EventToolResult,
		EventUsage, EventToolUse, EventToolResult,
		EventText, EventUsage, EventDone,
	}
	if got := rec.types(); fmt.Sprint(got) != fmt.Sprint(wantTypes) {
		t.Errorf("events:\n got %v\nwant %v", got, wantTypes)
	}
	if last := rec.last(); last.StopReason != "end_turn" {
		t.Errorf("done stop reason: %q", last.StopReason)
	}
}

func TestRunConcurrentReadOnly(t *testing.T) {
	const n = 3
	var arrived atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	// Each call parks until all n have arrived; that only happens when
	// they run at the same time.
	barrier := NewTool("probe", "", nil, true, func(ctx context.Context, _ json.RawMessage) (Result, error) {
		if arrived.Add(1) == n {
			once.Do(func() { close(release) })
		}
		select {
		case <-release:
			return Result{Content: "ok"}, nil
		case <-time.After(2 * time.Second):
			return Result{Content: "timeout"}, nil
		}
	})
	p := &scriptedProvider{steps: []scriptStep{
		{turn: toolUseTurn(call("1", "probe", `{}`), call("2", "probe", `{}`), call("3", "probe", `{}`))},
		{turn: endTurn("done")},
	}}
	agent, _ := New(Options{Provider: p, Tools: []Tool{barrier}})
	session := &Session{}
	if err := agent.Run(context.Background(), session, "go", nil); err != nil {
		t.Fatal(err)
	}
	for _, r := range session.Messages[2].ToolResults {
		if r.Result.Content != "ok" {
			t.Fatalf("read-only calls did not run concurrently: %+v", r)
		}
	}
}

func TestRunMixedCallsAreSequential(t *testing.T) {
	var running atomic.Int32
	var overlapped atomic.Bool
	var order []string
	var mu sync.Mutex
	mk := func(name string, readOnly bool) Tool {
		return NewTool(name, "", nil, readOnly, func(ctx context.Context, _ json.RawMessage) (Result, error) {
			if running.Add(1) > 1 {
				overlapped.Store(true)
			}
			time.Sleep(10 * time.Millisecond)
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			running.Add(-1)
			return Result{Content: name}, nil
		})
	}
	p := &scriptedProvider{steps: []scriptStep{
		{turn: toolUseTurn(call("1", "read", `{}`), call("2", "write", `{}`), call("3", "read", `{}`))},
		{turn: endTurn("done")},
	}}
	agent, _ := New(Options{
		Provider: p,
		Tools:    []Tool{mk("read", true), mk("write", false)},
		Approve:  func(context.Context, ToolCall, Tool) bool { return true },
	})
	if err := agent.Run(context.Background(), &Session{}, "go", nil); err != nil {
		t.Fatal(err)
	}
	if overlapped.Load() {
		t.Error("calls overlapped although one was a write")
	}
	if fmt.Sprint(order) != "[read write read]" {
		t.Errorf("order: %v", order)
	}
}

func TestRunDeniesWritesByDefault(t *testing.T) {
	var executed atomic.Bool
	write := NewTool("write", "", nil, false, func(ctx context.Context, _ json.RawMessage) (Result, error) {
		executed.Store(true)
		return Result{Content: "wrote"}, nil
	})
	p := &scriptedProvider{steps: []scriptStep{
		{turn: toolUseTurn(call("1", "write", `{}`), call("2", "read", `{}`))},
		{turn: endTurn("done")},
	}}
	agent, _ := New(Options{Provider: p, Tools: []Tool{write, echoTool("read", true)}})
	session := &Session{}
	rec := &recorder{}
	if err := agent.Run(context.Background(), session, "go", rec.emit); err != nil {
		t.Fatal(err)
	}
	if executed.Load() {
		t.Error("write tool ran without approval")
	}
	results := session.Messages[2].ToolResults
	if len(results) != 2 {
		t.Fatalf("results: %+v", results)
	}
	if !results[0].Denied || !results[0].Result.IsError || results[0].Result.Content != "tool write was not approved" {
		t.Errorf("denied result: %+v", results[0])
	}
	if results[1].Denied || results[1].Result.IsError {
		t.Errorf("read-only result should be allowed: %+v", results[1])
	}
	// The denial reaches the stream too.
	for _, e := range rec.events {
		if e.Type == EventToolResult && e.Result.ID == "1" && !e.Result.Denied {
			t.Error("tool_result event for the denied call is not marked denied")
		}
	}
}

func TestRunUnknownToolAndPanic(t *testing.T) {
	boom := NewTool("boom", "", nil, true, func(ctx context.Context, _ json.RawMessage) (Result, error) {
		panic("kaboom")
	})
	failing := NewTool("fail", "", nil, true, func(ctx context.Context, _ json.RawMessage) (Result, error) {
		return Result{}, errors.New("disk on fire")
	})
	p := &scriptedProvider{steps: []scriptStep{
		{turn: toolUseTurn(call("1", "nope", `{}`), call("2", "boom", `{}`), call("3", "fail", `{}`))},
		{turn: endTurn("done")},
	}}
	agent, _ := New(Options{Provider: p, Tools: []Tool{boom, failing}})
	session := &Session{}
	if err := agent.Run(context.Background(), session, "go", nil); err != nil {
		t.Fatal(err)
	}
	results := session.Messages[2].ToolResults
	checks := []struct{ want string }{{`unknown tool "nope"`}, {"tool boom panicked: kaboom"}, {"tool fail failed: disk on fire"}}
	for i, c := range checks {
		if !results[i].Result.IsError || results[i].Result.Content != c.want {
			t.Errorf("result %d: got %+v, want error %q", i, results[i].Result, c.want)
		}
	}
}

func TestRunTruncatesToolResults(t *testing.T) {
	big := strings.Repeat("x", 3000)
	tool := NewTool("big", "", nil, true, func(ctx context.Context, _ json.RawMessage) (Result, error) {
		return Result{Content: big, JSON: json.RawMessage(`{"a":1}`)}, nil
	})
	p := &scriptedProvider{steps: []scriptStep{
		{turn: toolUseTurn(call("1", "big", `{}`))},
		{turn: endTurn("done")},
	}}
	agent, _ := New(Options{Provider: p, Tools: []Tool{tool}, MaxToolResultBytes: 2500})
	session := &Session{}
	rec := &recorder{}
	if err := agent.Run(context.Background(), session, "go", rec.emit); err != nil {
		t.Fatal(err)
	}
	// What the model got: the result message content built from the full results.
	var sent []ToolResult
	if err := json.Unmarshal(session.Messages[2].Content, &sent); err != nil {
		t.Fatal(err)
	}
	wantSuffix := "\n[truncated: 2500 of 3000 bytes shown]"
	if !strings.HasSuffix(sent[0].Result.Content, wantSuffix) || len(sent[0].Result.Content) != 2500+len(wantSuffix) {
		t.Errorf("model content: len %d, tail %q", len(sent[0].Result.Content), sent[0].Result.Content[len(sent[0].Result.Content)-50:])
	}
	// What the transcript keeps: cut again to the display limit.
	disp := session.Messages[2].ToolResults[0].Result
	if !strings.HasSuffix(disp.Content, "bytes shown]") || len(disp.Content) > DisplayToolResultBytes+60 || disp.JSON != nil {
		t.Errorf("display content: len %d json %v", len(disp.Content), disp.JSON)
	}
	if !strings.Contains(disp.Content, fmt.Sprintf("[truncated: %d of", DisplayToolResultBytes)) {
		t.Errorf("display marker: %q", disp.Content[len(disp.Content)-60:])
	}
}

func TestTruncateRuneBoundary(t *testing.T) {
	s := "abécd" // e-acute is two bytes at offset 2..4
	got := Truncate(s, 3)
	if !strings.HasPrefix(got, "ab\n[truncated: 2 of 6 bytes shown]") {
		t.Errorf("got %q", got)
	}
	if Truncate("short", 10) != "short" {
		t.Error("short strings must pass through")
	}
}

func TestRunMaxTurns(t *testing.T) {
	steps := make([]scriptStep, 0, 10)
	for i := 0; i < 10; i++ {
		steps = append(steps, scriptStep{turn: toolUseTurn(call(fmt.Sprint(i), "read", `{}`))})
	}
	p := &scriptedProvider{steps: steps}
	agent, _ := New(Options{Provider: p, Tools: []Tool{echoTool("read", true)}, MaxTurns: 3})
	session := &Session{}
	rec := &recorder{}
	if err := agent.Run(context.Background(), session, "go", rec.emit); err != nil {
		t.Fatal(err)
	}
	if len(p.inputs) != 3 {
		t.Errorf("turns: %d", len(p.inputs))
	}
	if last := rec.last(); last.Type != EventDone || last.StopReason != StopReasonMaxTurns {
		t.Errorf("last event: %+v", last)
	}
	// user + 3 x (assistant, results)
	if len(session.Messages) != 7 {
		t.Errorf("messages: %d", len(session.Messages))
	}
}

func TestRunProviderError(t *testing.T) {
	boom := errors.New("upstream 500")
	p := &scriptedProvider{steps: []scriptStep{{err: boom}}}
	agent, _ := New(Options{Provider: p})
	session := &Session{}
	rec := &recorder{}
	err := agent.Run(context.Background(), session, "go", rec.emit)
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if last := rec.last(); last.Type != EventError || !strings.Contains(last.Error, "upstream 500") {
		t.Errorf("last event: %+v", last)
	}
	if len(session.Messages) != 1 {
		t.Errorf("only the user message should be appended, got %d", len(session.Messages))
	}
}

func TestRunContextCancelled(t *testing.T) {
	t.Run("before the first turn", func(t *testing.T) {
		p := &scriptedProvider{steps: []scriptStep{{turn: endTurn("x")}}}
		agent, _ := New(Options{Provider: p})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		rec := &recorder{}
		err := agent.Run(ctx, &Session{}, "go", rec.emit)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
		if last := rec.last(); last.Type != EventError {
			t.Errorf("last event: %+v", last)
		}
	})
	t.Run("during the provider turn", func(t *testing.T) {
		p := &scriptedProvider{steps: []scriptStep{{blockUntilClose: true}}}
		agent, _ := New(Options{Provider: p})
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(20 * time.Millisecond); cancel() }()
		err := agent.Run(ctx, &Session{}, "go", nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("during a tool call", func(t *testing.T) {
		slow := NewTool("slow", "", nil, true, func(ctx context.Context, _ json.RawMessage) (Result, error) {
			<-ctx.Done()
			return Result{}, ctx.Err()
		})
		p := &scriptedProvider{steps: []scriptStep{{turn: toolUseTurn(call("1", "slow", `{}`))}, {turn: endTurn("x")}}}
		agent, _ := New(Options{Provider: p, Tools: []Tool{slow}})
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(20 * time.Millisecond); cancel() }()
		session := &Session{}
		rec := &recorder{}
		err := agent.Run(ctx, session, "go", rec.emit)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
		// The tool result message is still appended so the history stays valid.
		if len(session.Messages) != 3 || session.Messages[2].Role != RoleUser {
			t.Errorf("messages: %d", len(session.Messages))
		}
		if last := rec.last(); last.Type != EventError {
			t.Errorf("last event: %+v", last)
		}
	})
}

func TestRunNilEmitAndNilSession(t *testing.T) {
	p := &scriptedProvider{steps: []scriptStep{{turn: endTurn("x")}}}
	agent, _ := New(Options{Provider: p})
	if err := agent.Run(context.Background(), nil, "go", nil); err == nil {
		t.Error("nil session must fail")
	}
	if err := agent.Run(context.Background(), &Session{}, "go", nil); err != nil {
		t.Errorf("nil emit must be allowed: %v", err)
	}
}

func TestRunAnswersToolCallsAfterNonToolUseStop(t *testing.T) {
	for _, reason := range []string{"max_tokens", "refusal", "pause_turn"} {
		turn := toolUseTurn(call("c1", "read", `{}`), call("c2", "read", `{}`))
		turn.StopReason = reason
		p := &scriptedProvider{steps: []scriptStep{{turn: turn}}}
		var ran atomic.Int32
		read := NewTool("read", "read", nil, true, func(context.Context, json.RawMessage) (Result, error) {
			ran.Add(1)
			return Result{Content: "x"}, nil
		})
		agent, err := New(Options{Provider: p, Tools: []Tool{read}})
		if err != nil {
			t.Fatal(err)
		}
		session := &Session{}
		rec := &recorder{}
		if err := agent.Run(context.Background(), session, "go", rec.emit); err != nil {
			t.Fatalf("%s: run: %v", reason, err)
		}
		if ran.Load() != 0 {
			t.Errorf("%s: calls of a turn that did not stop for tool_use must not run", reason)
		}
		if last := rec.last(); last.Type != EventDone || last.StopReason != reason {
			t.Errorf("%s: last event = %+v", reason, last)
		}
		if len(session.Messages) != 3 || session.Messages[1].Role != RoleAssistant || session.Messages[2].Role != RoleUser {
			t.Fatalf("%s: history = %+v", reason, session.Messages)
		}
		results := session.Messages[2].ToolResults
		want := "not run: the turn ended with stop reason " + reason
		if len(results) != 2 || results[0].ID != "c1" || results[1].ID != "c2" {
			t.Fatalf("%s: results = %+v", reason, results)
		}
		for _, r := range results {
			if !r.Result.IsError || r.Result.Content != want {
				t.Errorf("%s: result = %+v", reason, r)
			}
		}
		if !strings.Contains(string(session.Messages[2].Content), want) {
			t.Errorf("%s: the native tool result message must carry the error: %s", reason, session.Messages[2].Content)
		}
	}
}

func TestRepairDanglingToolCalls(t *testing.T) {
	p := &scriptedProvider{}
	if RepairDanglingToolCalls(nil, p) || RepairDanglingToolCalls(&Session{}, p) {
		t.Error("nothing to repair in an empty session")
	}
	clean := &Session{Messages: []Message{
		p.UserMessage("hi"),
		{Role: RoleAssistant, ToolCalls: []ToolCall{call("c1", "read", `{}`)}},
		p.ToolResultMessage([]ToolResult{{ID: "c1"}}),
		{Role: RoleAssistant, Text: "done"},
	}}
	if RepairDanglingToolCalls(clean, p) || len(clean.Messages) != 4 {
		t.Error("a session ending in an answer must not be repaired")
	}
	waiting := &Session{Messages: []Message{p.UserMessage("hi")}}
	if RepairDanglingToolCalls(waiting, p) || len(waiting.Messages) != 1 {
		t.Error("a session ending in a user message must not be repaired")
	}

	dangling := &Session{Messages: []Message{
		p.UserMessage("hi"),
		{Role: RoleAssistant, Content: json.RawMessage(`{"assistant":true}`), ToolCalls: []ToolCall{call("c1", "read", `{}`), call("c2", "write", `{}`)}},
	}}
	before := string(dangling.Messages[1].Content)
	if !RepairDanglingToolCalls(dangling, p) {
		t.Fatal("dangling tool calls must be repaired")
	}
	if len(dangling.Messages) != 3 || string(dangling.Messages[1].Content) != before {
		t.Fatalf("repair must only append: %+v", dangling.Messages)
	}
	m := dangling.Messages[2]
	if m.Role != RoleUser || m.CreatedAt.IsZero() || len(m.ToolResults) != 2 {
		t.Fatalf("repair message = %+v", m)
	}
	for i, id := range []string{"c1", "c2"} {
		r := m.ToolResults[i]
		if r.ID != id || !r.Result.IsError || r.Result.Content != "not run: the previous run was interrupted" {
			t.Errorf("result %d = %+v", i, r)
		}
	}
	if RepairDanglingToolCalls(dangling, p) {
		t.Error("a repaired session must not be repaired again")
	}
}
