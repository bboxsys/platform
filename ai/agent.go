package ai

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bboxsys/platform/logger"
)

const (
	// DefaultMaxTurns bounds the number of model turns in one Run.
	DefaultMaxTurns = 25
	// DefaultMaxToolResultBytes bounds the content of one tool result as
	// the model sees it.
	DefaultMaxToolResultBytes = 64 * 1024
	// DisplayToolResultBytes bounds the copy of a tool result kept in
	// Message.ToolResults for transcripts.
	DisplayToolResultBytes = 2000
	// StopReasonMaxTurns is the stop reason EventDone carries when a run
	// ends because MaxTurns was reached.
	StopReasonMaxTurns = "max_turns"
)

// ErrNoProvider is returned by New when Options.Provider is nil.
var ErrNoProvider = errors.New("ai: provider is required")

// Options configures an Agent.
type Options struct {
	// Provider runs each model turn. Required.
	Provider Provider
	// Tools are offered to the model, in this order. Names must be unique
	// and match ToolNamePattern.
	Tools []Tool
	// System is the system prompt. Keep it stable within a session so the
	// provider can cache it.
	System string
	// MaxTurns bounds the model turns of one Run; zero means DefaultMaxTurns.
	MaxTurns int
	// Approve decides whether a requested call runs. nil allows read-only
	// tools and denies everything else.
	Approve func(ctx context.Context, call ToolCall, t Tool) bool
	// MaxToolResultBytes truncates a tool result's content before the model
	// sees it; zero means DefaultMaxToolResultBytes.
	MaxToolResultBytes int
	// Logger receives debug lines about the loop; nil is silent.
	Logger *logger.Logger
}

// Agent runs prompts against a provider and a tool set. It is safe for
// concurrent use; per-run state lives in the Session.
type Agent struct {
	opts     Options
	registry *Registry
	tools    []Tool
}

// New validates o and returns an Agent.
func New(o Options) (*Agent, error) {
	if o.Provider == nil {
		return nil, ErrNoProvider
	}
	registry := NewRegistry()
	if err := registry.Add(o.Tools...); err != nil {
		return nil, fmt.Errorf("ai: tools: %w", err)
	}
	if o.MaxTurns <= 0 {
		o.MaxTurns = DefaultMaxTurns
	}
	if o.MaxToolResultBytes <= 0 {
		o.MaxToolResultBytes = DefaultMaxToolResultBytes
	}
	return &Agent{opts: o, registry: registry, tools: registry.List()}, nil
}

// Run appends prompt to the session as a user message and drives the loop
// until the model stops without tool calls, MaxTurns is reached, the
// context is cancelled, or the provider fails. Every message produced is
// appended to session.Messages; the caller persists the session afterwards.
// emit receives every Event of the run and may be nil.
//
// A provider error or a cancelled context ends the run with EventError and
// the error; every other outcome ends with EventDone and a nil error.
func (a *Agent) Run(ctx context.Context, session *Session, prompt string, emit func(Event)) error {
	if session == nil {
		return errors.New("ai: session is required")
	}
	emit = serialized(emit)
	provider := a.opts.Provider

	a.append(session, provider.UserMessage(prompt))

	for turn := 0; turn < a.opts.MaxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			emit(Event{Type: EventError, Error: err.Error()})
			return err
		}
		in := TurnInput{
			System:   a.opts.System,
			Messages: session.Messages,
			Tools:    a.tools,
			Model:    session.Model,
		}
		t, err := provider.Turn(ctx, in, emit)
		if err != nil {
			// A cancelled context surfaces as whatever the transport wrapped
			// it in; report the cancellation itself so callers can test for it.
			if cerr := ctx.Err(); cerr != nil {
				emit(Event{Type: EventError, Error: cerr.Error()})
				return cerr
			}
			err = fmt.Errorf("ai: provider %s: %w", provider.Name(), err)
			emit(Event{Type: EventError, Error: err.Error()})
			return err
		}

		a.append(session, t.Message)
		if session.Provider == "" {
			session.Provider = provider.Name()
		}
		if session.Model == "" {
			session.Model = provider.Model()
		}
		session.Usage.Add(t.Usage)
		usage := t.Usage
		emit(Event{Type: EventUsage, Usage: &usage})
		a.debug("turn", turn+1, t.StopReason, len(t.ToolCalls))

		if t.StopReason != StopReasonToolUse || len(t.ToolCalls) == 0 {
			if len(t.ToolCalls) > 0 {
				// The turn requested calls but ended for another reason
				// (max_tokens mid-call, refusal, pause_turn). Answer each
				// with an error result without running it: an unanswered
				// tool_use is a history the model API rejects on every
				// later request, and history is never rewritten.
				a.append(session, notRunMessage(provider, t.ToolCalls, "not run: the turn ended with stop reason "+t.StopReason))
			}
			emit(Event{Type: EventDone, StopReason: t.StopReason})
			return nil
		}

		results := a.runCalls(ctx, t.ToolCalls, emit)
		// The results are appended even when the context was cancelled
		// while the tools ran: an assistant message with tool_use blocks
		// that has no matching tool_result message is a history the model
		// API rejects, and history is never rewritten.
		msg := provider.ToolResultMessage(results)
		msg.ToolResults = DisplayToolResults(results)
		a.append(session, msg)

		if err := ctx.Err(); err != nil {
			emit(Event{Type: EventError, Error: err.Error()})
			return err
		}
	}

	emit(Event{Type: EventDone, StopReason: StopReasonMaxTurns})
	return nil
}

// RepairDanglingToolCalls makes a session whose last message is an
// assistant message with tool calls valid to continue: that history ends
// in tool_use blocks with no tool_result, which the model API rejects, and
// it is what a process that stopped while tools ran leaves on disk. It
// appends one tool result message answering every call with an error
// result and reports whether it did; nothing earlier is edited. Call it
// on a loaded session before Run.
func RepairDanglingToolCalls(session *Session, provider Provider) bool {
	if session == nil || provider == nil || len(session.Messages) == 0 {
		return false
	}
	last := session.Messages[len(session.Messages)-1]
	if last.Role != RoleAssistant || len(last.ToolCalls) == 0 {
		return false
	}
	m := notRunMessage(provider, last.ToolCalls, "not run: the previous run was interrupted")
	m.CreatedAt = time.Now().UTC()
	session.Messages = append(session.Messages, m)
	session.UpdatedAt = m.CreatedAt
	return true
}

// notRunMessage is the tool result message answering every call with an
// error result carrying reason, for calls that were never run.
func notRunMessage(provider Provider, calls []ToolCall, reason string) Message {
	results := make([]ToolResult, len(calls))
	for i, c := range calls {
		results[i] = ToolResult{ID: c.ID, Name: c.Name, Result: ErrorResult("%s", reason)}
	}
	msg := provider.ToolResultMessage(results)
	msg.ToolResults = DisplayToolResults(results)
	return msg
}

// append adds m to the session and stamps both.
func (a *Agent) append(session *Session, m Message) {
	now := time.Now().UTC()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	session.Messages = append(session.Messages, m)
	session.UpdatedAt = now
}

// runCalls executes one turn's calls. When every call is for a read-only
// tool they run concurrently; otherwise they run in order, so a write sees
// the effect of the calls before it.
func (a *Agent) runCalls(ctx context.Context, calls []ToolCall, emit func(Event)) []ToolResult {
	results := make([]ToolResult, len(calls))
	if len(calls) > 1 && a.allReadOnly(calls) {
		for i := range calls {
			call := calls[i]
			emit(Event{Type: EventToolUse, Call: &call})
		}
		var wg sync.WaitGroup
		for i := range calls {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				results[i] = a.runCall(ctx, calls[i])
				r := results[i]
				emit(Event{Type: EventToolResult, Result: &r})
			}(i)
		}
		wg.Wait()
		return results
	}
	for i := range calls {
		call := calls[i]
		emit(Event{Type: EventToolUse, Call: &call})
		results[i] = a.runCall(ctx, call)
		r := results[i]
		emit(Event{Type: EventToolResult, Result: &r})
	}
	return results
}

// allReadOnly reports whether every call names a registered read-only tool.
func (a *Agent) allReadOnly(calls []ToolCall) bool {
	for _, c := range calls {
		t, ok := a.registry.Get(c.Name)
		if !ok || !t.ReadOnly() {
			return false
		}
	}
	return true
}

// runCall resolves, approves, executes, and truncates one call. Every
// failure becomes an error result the model reads; nothing here returns an
// error to the caller of Run.
func (a *Agent) runCall(ctx context.Context, call ToolCall) ToolResult {
	out := ToolResult{ID: call.ID, Name: call.Name}
	t, ok := a.registry.Get(call.Name)
	if !ok {
		out.Result = ErrorResult("unknown tool %q", call.Name)
		return out
	}
	if !a.approve(ctx, call, t) {
		out.Denied = true
		out.Result = ErrorResult("tool %s was not approved", call.Name)
		a.debug("denied", call.Name)
		return out
	}
	res := execute(ctx, t, call)
	res.Content = Truncate(res.Content, a.opts.MaxToolResultBytes)
	out.Result = res
	return out
}

func (a *Agent) approve(ctx context.Context, call ToolCall, t Tool) bool {
	if a.opts.Approve == nil {
		return t.ReadOnly()
	}
	return a.opts.Approve(ctx, call, t)
}

// execute runs the tool and converts an error or a panic into an error
// result. A panic in one tool must not take down the service hosting the
// loop, and the model can often recover when told what went wrong.
func execute(ctx context.Context, t Tool, call ToolCall) (res Result) {
	defer func() {
		if r := recover(); r != nil {
			res = ErrorResult("tool %s panicked: %v", call.Name, r)
		}
	}()
	input := call.Input
	if len(input) == 0 {
		input = []byte("{}")
	}
	res, err := t.Execute(ctx, input)
	if err != nil {
		return ErrorResult("tool %s failed: %v", call.Name, err)
	}
	return res
}

// Truncate cuts s to at most limit bytes on a rune boundary and appends a
// marker saying how much was shown. s is returned unchanged when it fits.
func Truncate(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return fmt.Sprintf("%s\n[truncated: %d of %d bytes shown]", s[:cut], cut, len(s))
}

// DisplayToolResults returns copies of results with Content cut to
// DisplayToolResultBytes and JSON dropped, for Message.ToolResults.
func DisplayToolResults(results []ToolResult) []ToolResult {
	if results == nil {
		return nil
	}
	out := make([]ToolResult, len(results))
	for i, r := range results {
		r.Result.JSON = nil
		r.Result.Content = Truncate(r.Result.Content, DisplayToolResultBytes)
		out[i] = r
	}
	return out
}

// serialized wraps emit so concurrent tool goroutines never call it at the
// same time; an SSE writer is not safe for concurrent use. A nil emit
// becomes a no-op.
func serialized(emit func(Event)) func(Event) {
	if emit == nil {
		return func(Event) {}
	}
	var mu sync.Mutex
	return func(e Event) {
		mu.Lock()
		defer mu.Unlock()
		emit(e)
	}
}

func (a *Agent) debug(msg string, fields ...any) {
	if a.opts.Logger == nil {
		return
	}
	a.opts.Logger.DebugE().Any("fields", fields).Msg("ai: " + msg)
}
