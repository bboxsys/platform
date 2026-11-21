package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sync"
)

// Tool is one action the model may request. The loop decides whether to
// run it (Options.Approve), runs it, and returns Result to the model.
type Tool interface {
	// Name is the identifier the model calls. It must match ToolNamePattern.
	Name() string
	// Description tells the model what the tool does and when to use it.
	Description() string
	// InputSchema is a JSON Schema object describing the input.
	InputSchema() json.RawMessage
	// ReadOnly reports that the tool observes without changing anything.
	// Read-only calls in one turn may run concurrently and are allowed by
	// the default approval policy; everything else is treated as a write.
	ReadOnly() bool
	// Execute runs the tool. The context carries the Caller (WithCaller)
	// so a tool can act with the user's own identity. An error is reported
	// to the model as a failed tool result, not returned to the caller of
	// the loop; return a Result with IsError for a failure the model should
	// read verbatim.
	Execute(ctx context.Context, input json.RawMessage) (Result, error)
}

// Result is what a tool returns to the model.
type Result struct {
	// Content is the text the model reads.
	Content string `json:"content"`
	// JSON is optional structured content for callers that can use it
	// (MCP structuredContent). The model sees Content.
	JSON json.RawMessage `json:"json,omitempty"`
	// IsError marks a failed call so the model can recover.
	IsError bool `json:"is_error,omitempty"`
}

// ErrorResult builds a failed Result from a message.
func ErrorResult(format string, args ...any) Result {
	return Result{Content: fmt.Sprintf(format, args...), IsError: true}
}

// ToolNamePattern is what a tool name must match: the Anthropic and MCP
// limits are both satisfied by this subset.
var ToolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// ErrDuplicateTool is returned by Registry.Add for a name already present.
var ErrDuplicateTool = errors.New("ai: duplicate tool name")

// ErrInvalidToolName is returned for a name that does not match ToolNamePattern.
var ErrInvalidToolName = errors.New("ai: invalid tool name")

// FuncTool is a Tool built from a function.
type FuncTool struct {
	name        string
	description string
	schema      json.RawMessage
	readOnly    bool
	fn          func(ctx context.Context, input json.RawMessage) (Result, error)
}

// NewTool builds a Tool from a function. schema must be a JSON Schema
// object; nil means an object with no properties.
func NewTool(name, description string, schema json.RawMessage, readOnly bool, fn func(ctx context.Context, input json.RawMessage) (Result, error)) *FuncTool {
	if len(schema) == 0 {
		schema = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return &FuncTool{name: name, description: description, schema: schema, readOnly: readOnly, fn: fn}
}

func (t *FuncTool) Name() string                 { return t.name }
func (t *FuncTool) Description() string          { return t.description }
func (t *FuncTool) InputSchema() json.RawMessage { return t.schema }
func (t *FuncTool) ReadOnly() bool               { return t.readOnly }
func (t *FuncTool) Execute(ctx context.Context, input json.RawMessage) (Result, error) {
	if t.fn == nil {
		return ErrorResult("tool %s has no implementation", t.name), nil
	}
	return t.fn(ctx, input)
}

// Registry is a named set of tools in registration order.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	order []string
}

// NewRegistry returns a registry holding tools. It panics on a duplicate
// or invalid name, which is a programming error at startup; use Add for
// tools discovered at runtime.
func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{tools: make(map[string]Tool)}
	if err := r.Add(tools...); err != nil {
		panic(err)
	}
	return r
}

// Add registers tools. On error nothing is added.
func (r *Registry) Add(tools ...Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := make(map[string]struct{}, len(tools))
	for _, t := range tools {
		name := t.Name()
		if !ToolNamePattern.MatchString(name) {
			return fmt.Errorf("%w: %q", ErrInvalidToolName, name)
		}
		if _, dup := r.tools[name]; dup {
			return fmt.Errorf("%w: %q", ErrDuplicateTool, name)
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("%w: %q", ErrDuplicateTool, name)
		}
		seen[name] = struct{}{}
	}
	for _, t := range tools {
		r.tools[t.Name()] = t
		r.order = append(r.order, t.Name())
	}
	return nil
}

// Get returns the tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns the tools in registration order.
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.tools[name])
	}
	return out
}

// Names returns the registered names sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := append([]string(nil), r.order...)
	sort.Strings(names)
	return names
}

// Caller is the identity a run acts as. Tools that call the platform's
// own HTTP API forward Bearer and the account so the service's normal
// authorization applies; nothing in the loop grants more than the caller
// already has.
type Caller struct {
	AccountID string
	// UserID is the user's NATS public key, as admin.UserIDFromContext
	// returns it after Bearer.
	UserID string
	// Bearer is the caller's own token, forwarded to API tools.
	Bearer string
}

type callerKey struct{}

// WithCaller returns a context carrying the caller.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFromContext returns the caller placed by WithCaller.
func CallerFromContext(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok
}
