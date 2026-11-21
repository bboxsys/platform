package ai

import (
	"encoding/json"
	"time"
)

// Role is who a message is from. Tool results travel in a user message,
// as the model APIs require.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one entry of a session's history.
//
// Content is the provider's native content-block JSON and is what goes
// back to the model verbatim; it is never edited after it is appended.
// The remaining fields are a display projection filled when the message
// is created, so a transcript can be rendered without parsing Content.
type Message struct {
	Role    Role            `json:"role"`
	Content json.RawMessage `json:"content"`
	// Text is the user's prompt or the assistant's visible text.
	Text string `json:"text,omitempty"`
	// ToolCalls are the calls an assistant message requested.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolResults are the results a user message carries back, with
	// Content truncated for display.
	ToolResults []ToolResult `json:"tool_results,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
}

// ToolCall is one tool request from the model.
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolResult is the outcome of one ToolCall.
type ToolResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Result Result `json:"result"`
	// Denied is set when the approval policy refused the call; Result then
	// carries the message the model was given.
	Denied bool `json:"denied,omitempty"`
}

// EventType names what an Event carries.
type EventType string

const (
	// EventText is a delta of the assistant's visible text.
	EventText EventType = "text"
	// EventThinking is a delta of the assistant's summarized reasoning.
	EventThinking EventType = "thinking"
	// EventToolUse announces a complete tool call before it runs.
	EventToolUse EventType = "tool_use"
	// EventToolResult reports a finished (or denied) tool call.
	EventToolResult EventType = "tool_result"
	// EventUsage reports the token usage of one model turn.
	EventUsage EventType = "usage"
	// EventCompaction reports that the provider compacted earlier context.
	EventCompaction EventType = "compaction"
	// EventDone ends a run; StopReason says why.
	EventDone EventType = "done"
	// EventError ends a run with a failure.
	EventError EventType = "error"
)

// Event is one item of the stream a run emits. Only the fields relevant
// to Type are set.
type Event struct {
	Type EventType `json:"type"`
	// Text is the delta for EventText and EventThinking, and the summary
	// for EventCompaction.
	Text string `json:"text,omitempty"`
	// Call is set for EventToolUse.
	Call *ToolCall `json:"call,omitempty"`
	// Result is set for EventToolResult.
	Result *ToolResult `json:"result,omitempty"`
	// Usage is set for EventUsage.
	Usage *Usage `json:"usage,omitempty"`
	// StopReason is set for EventDone: end_turn, max_tokens, refusal,
	// max_turns, or a provider-specific reason.
	StopReason string `json:"stop_reason,omitempty"`
	// Error is set for EventError.
	Error string `json:"error,omitempty"`
}

// Usage counts tokens. Fields are cumulative when read from a Session and
// per turn when read from an EventUsage.
type Usage struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

// Add accumulates u into the receiver.
func (u *Usage) Add(o Usage) {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CacheReadTokens += o.CacheReadTokens
	u.CacheWriteTokens += o.CacheWriteTokens
}
