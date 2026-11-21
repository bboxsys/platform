package ai

import "context"

// Provider runs one model turn. Implementations own the wire format of
// Message.Content, so a session is bound to the provider that created it
// (Session.Provider).
type Provider interface {
	// Name is the provider id, for example "anthropic".
	Name() string
	// Model is the model the provider will use when TurnInput.Model is empty.
	Model() string
	// UserMessage builds a user message from plain text.
	UserMessage(text string) Message
	// ToolResultMessage builds the user message that returns tool results
	// to the model. All results of one turn go in one message.
	ToolResultMessage(results []ToolResult) Message
	// Turn sends the history and tools to the model and returns the
	// assistant message to append. It calls emit for every streamed event
	// it can report (text, thinking, tool_use, usage, compaction); it must
	// not emit done or error, which belong to the loop. emit may be nil.
	Turn(ctx context.Context, in TurnInput, emit func(Event)) (*Turn, error)
}

// TurnInput is one model request.
type TurnInput struct {
	// System is the system prompt. Keep it stable across turns of a
	// session so the provider can cache it.
	System string
	// Messages is the whole history, oldest first, ending with a user
	// message.
	Messages []Message
	// Tools are offered in this order.
	Tools []Tool
	// Model overrides the provider's default when set.
	Model string
	// MaxTokens bounds the output; zero means the provider's default.
	MaxTokens int
}

// Turn is the result of one model request.
type Turn struct {
	// Message is the assistant message to append verbatim.
	Message Message
	// Text is the visible text of the message.
	Text string
	// ToolCalls are the calls requested, in order. They normally come with
	// StopReason "tool_use"; a turn cut off by another stop reason
	// (max_tokens, refusal) can still carry calls, which the loop answers
	// with error results instead of running so the history stays valid.
	ToolCalls []ToolCall
	// StopReason is the provider's stop reason: end_turn, tool_use,
	// max_tokens, refusal, or pause_turn.
	StopReason string
	// Usage is this turn's token usage.
	Usage Usage
}

// StopReasonToolUse is the stop reason that keeps the loop going.
const StopReasonToolUse = "tool_use"

// ProviderFactory builds a provider from a resolved credential. Hosts keep
// one factory per Credential.Provider value.
type ProviderFactory func(cred Credential) (Provider, error)

// Checker is implemented by providers that can verify their credential
// without running a model turn, for a "test this key" action. Check must
// not spend output tokens.
type Checker interface {
	Check(ctx context.Context) (CheckResult, error)
}

// CheckResult describes what a successful check found.
type CheckResult struct {
	// Model is the model id the credential will use.
	Model string `json:"model,omitempty"`
	// DisplayName is the provider's name for the model.
	DisplayName string `json:"display_name,omitempty"`
	// ContextWindow is the model's maximum input tokens, when known.
	ContextWindow int64 `json:"context_window,omitempty"`
	// MaxOutput is the model's maximum output tokens, when known.
	MaxOutput int64 `json:"max_output,omitempty"`
}

// ModelLister is implemented by providers that can list the models their
// endpoint serves, so a form can offer them instead of asking for an id.
// ListModels must not spend output tokens; a failure is an *CheckError when
// it can be classified.
type ModelLister interface {
	ListModels(ctx context.Context) ([]ModelInfo, error)
}

// ModelInfo is one model a provider's endpoint offers.
type ModelInfo struct {
	// ID is the id to put in a credential's model tag.
	ID string `json:"id"`
	// DisplayName is the provider's name for it, when it has one.
	DisplayName string `json:"display_name,omitempty"`
	// ContextWindow is the context size in tokens, when the listing says.
	ContextWindow int64 `json:"context_window,omitempty"`
}

// CheckErrorKind classifies a failed credential check so a UI can say what
// to fix.
type CheckErrorKind string

const (
	// CheckAuth means the provider rejected the key.
	CheckAuth CheckErrorKind = "auth"
	// CheckNotFound means the key works but the model does not exist or is
	// not available to it.
	CheckNotFound CheckErrorKind = "not_found"
	// CheckUnreachable means the endpoint could not be reached.
	CheckUnreachable CheckErrorKind = "unreachable"
	// CheckRejected is any other refusal from the provider.
	CheckRejected CheckErrorKind = "rejected"
)

// CheckError is a classified check failure. Message never contains the
// credential's secret.
type CheckError struct {
	Kind    CheckErrorKind
	Message string
}

func (e *CheckError) Error() string { return e.Message }
