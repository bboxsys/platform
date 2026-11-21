// Package openai is the ai.Provider for servers that speak the OpenAI chat
// completions API: Ollama's /v1 endpoints, llama.cpp's llama-server, vLLM,
// LM Studio, and gateways in front of them. It serves two credential
// providers, ai.ProviderOllama (NewOllama, OllamaFactory: the endpoint is
// the Ollama server root, http://localhost:11434 by default) and
// ai.ProviderOpenAICompat (New, Factory: the endpoint is the API base URL
// ending in /v1 and is required).
//
// One request per turn, streamed. Tools are sent as function tools,
// reasoning deltas (reasoning_content, or reasoning as Ollama names it)
// are emitted as thinking, and the assistant message is stored in the
// wire format, so a session created here stays on this provider.
//
// Message.Content for this provider is a JSON array of chat messages: a
// user prompt is one user message, the results of a turn are one tool
// message per result, and an assistant turn is one assistant message
// carrying content, reasoning_content, and tool_calls. The history sent
// to the server is the concatenation.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bboxsys/platform/ai"
	"github.com/bboxsys/platform/logger"
)

const (
	// DefaultMaxTokens is the output cap when neither the options nor the
	// credential set one. Local models have smaller windows than hosted
	// ones, so it is lower than the Anthropic default.
	DefaultMaxTokens = 8192
	// maxErrorBody bounds how much of an error response is read.
	maxErrorBody = 64 << 10
	// maxLine bounds one SSE line; a tool argument delta is far smaller.
	maxLine = 4 << 20
)

// Options tunes the provider. The zero value sends no reasoning effort
// (the server's default) and caps output at DefaultMaxTokens.
type Options struct {
	// Effort is the reasoning effort sent as reasoning_effort. It
	// overrides the credential's ai_effort tag; empty means that tag, and
	// no tag means the field is not sent. xhigh and max are sent as high,
	// the highest value the OpenAI dialect defines.
	Effort string
	// MaxTokens caps output tokens per turn; zero means the credential's
	// ai_max_output tag or DefaultMaxTokens.
	MaxTokens int
	// HTTPClient replaces http.DefaultClient, for tests and proxies. A
	// turn can take minutes on a local model, so it should carry no
	// overall timeout; the request context bounds it.
	HTTPClient *http.Client
	// Logger receives debug lines; nil is silent.
	Logger *logger.Logger
}

// Provider is an ai.Provider for one credential.
type Provider struct {
	name      string
	base      string // the API base URL, without a trailing slash
	root      string // the Ollama server root, for /api/show; "" otherwise
	bearer    string
	model     string
	effort    string
	maxTokens int
	window    int
	client    *http.Client
	log       *logger.Logger
}

// New builds a provider for an openai_compat credential: Endpoint is the
// API base URL (for example http://llm.example/v1) and is required, Model
// is the model id, and Bearer, when set, is sent as the Authorization
// bearer.
func New(cred ai.Credential, opts Options) (*Provider, error) {
	base := strings.TrimRight(strings.TrimSpace(cred.Endpoint), "/")
	if base == "" {
		return nil, fmt.Errorf("openai: credential %q needs an endpoint (the API base URL ending in /v1)", cred.Name)
	}
	return newProvider(ai.ProviderOpenAICompat, base, "", cred, opts), nil
}

// NewOllama builds a provider for an ollama credential: Endpoint is the
// Ollama server root (ai.OllamaDefaultEndpoint when empty; a trailing /api
// or /v1 is accepted), Model is the model name as `ollama list` shows it,
// and Bearer is optional. It also works against any server that mounts the
// OpenAI API under /v1 of its root, such as llama-server.
func NewOllama(cred ai.Credential, opts Options) (*Provider, error) {
	root := ai.OllamaBaseURL(cred.Endpoint)
	return newProvider(ai.ProviderOllama, root+"/v1", root, cred, opts), nil
}

func newProvider(name, base, root string, cred ai.Credential, opts Options) *Provider {
	p := &Provider{
		name:   name,
		base:   base,
		root:   root,
		bearer: cred.Bearer,
		model:  strings.TrimSpace(cred.Model),
		window: cred.IntTag(ai.TagContextWindow, 0),
		client: opts.HTTPClient,
		log:    opts.Logger,
	}
	if p.client == nil {
		p.client = http.DefaultClient
	}
	p.effort = effort(opts.Effort)
	if p.effort == "" {
		p.effort = effort(cred.Tag(ai.TagEffort))
	}
	p.maxTokens = opts.MaxTokens
	if p.maxTokens <= 0 {
		p.maxTokens = cred.IntTag(ai.TagMaxOutput, DefaultMaxTokens)
	}
	return p
}

// Factory returns an ai.ProviderFactory for openai_compat credentials.
func Factory(opts Options) ai.ProviderFactory {
	return func(cred ai.Credential) (ai.Provider, error) { return New(cred, opts) }
}

// OllamaFactory returns an ai.ProviderFactory for ollama credentials.
func OllamaFactory(opts Options) ai.ProviderFactory {
	return func(cred ai.Credential) (ai.Provider, error) { return NewOllama(cred, opts) }
}

// effort maps the platform's effort levels onto the three the OpenAI
// dialect defines; anything else is not sent.
func effort(e string) string {
	switch e {
	case "low", "medium", "high":
		return e
	case "xhigh", "max":
		return "high"
	}
	return ""
}

// Name returns "ollama" or "openai_compat".
func (p *Provider) Name() string { return p.name }

// Model returns the model id in use; "" when the credential names none.
func (p *Provider) Model() string { return p.model }

// BaseURL returns the API base URL requests go to.
func (p *Provider) BaseURL() string { return p.base }

// chatMessage is one wire message. Content is always present: the dialect
// accepts "" beside tool_calls, and some servers reject null.
type chatMessage struct {
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []toolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// UserMessage builds a user message.
func (p *Provider) UserMessage(text string) ai.Message {
	content := mustMarshal([]chatMessage{{Role: "user", Content: text}})
	return ai.Message{Role: ai.RoleUser, Content: content, Text: text, CreatedAt: time.Now().UTC()}
}

// ToolResultMessage builds the message carrying one tool message per
// result, in order. The dialect has no error flag on a tool message, so a
// denied or failed call is prefixed so the model reads it as a failure.
func (p *Provider) ToolResultMessage(results []ai.ToolResult) ai.Message {
	msgs := make([]chatMessage, 0, len(results))
	for _, r := range results {
		text := r.Result.Content
		if text == "" {
			text = "(no output)"
		}
		if r.Result.IsError || r.Denied {
			text = "Error: " + text
		}
		msgs = append(msgs, chatMessage{Role: "tool", Content: text, ToolCallID: r.ID})
	}
	return ai.Message{
		Role:        ai.RoleUser,
		Content:     mustMarshal(msgs),
		ToolResults: ai.DisplayToolResults(results),
		CreatedAt:   time.Now().UTC(),
	}
}

// request is the chat completions body.
type request struct {
	Model           string            `json:"model"`
	Messages        []json.RawMessage `json:"messages"`
	Tools           []requestTool     `json:"tools,omitempty"`
	Stream          bool              `json:"stream"`
	StreamOptions   streamOptions     `json:"stream_options"`
	MaxTokens       int               `json:"max_tokens,omitempty"`
	ReasoningEffort string            `json:"reasoning_effort,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type requestTool struct {
	Type     string          `json:"type"`
	Function requestFunction `json:"function"`
}

type requestFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// chunk is one streamed server-sent event. Servers differ in details:
// content may be null, reasoning arrives as reasoning_content (llama.cpp,
// vLLM) or reasoning (Ollama), tool call deltas may omit index, and usage
// comes in a final chunk with no choices.
type chunk struct {
	Choices []struct {
		Delta struct {
			Content          *string `json:"content"`
			ReasoningContent string  `json:"reasoning_content"`
			Reasoning        string  `json:"reasoning"`
			ToolCalls        []struct {
				Index    *int         `json:"index"`
				ID       string       `json:"id"`
				Function toolFunction `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int64 `json:"prompt_tokens"`
		CompletionTokens    int64 `json:"completion_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *apiError `json:"error"`
}

// pendingCall accumulates one tool call across deltas.
type pendingCall struct {
	index int
	id    string
	name  string
	args  strings.Builder
}

// Turn streams one chat completion, emitting text and thinking deltas as
// they arrive, and returns the assistant message in the wire format.
func (p *Provider) Turn(ctx context.Context, in ai.TurnInput, emit func(ai.Event)) (*ai.Turn, error) {
	if emit == nil {
		emit = func(ai.Event) {}
	}
	model := in.Model
	if model == "" {
		model = p.model
	}
	if model == "" {
		return nil, fmt.Errorf("openai: the credential names no model (%s tag)", ai.TagModel)
	}
	body, err := p.request(model, in)
	if err != nil {
		return nil, err
	}
	resp, err := p.do(ctx, http.MethodPost, p.base+"/chat/completions", body, "text/event-stream")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var (
		text      strings.Builder
		reasoning strings.Builder
		calls     []*pendingCall
		finish    string
		usage     ai.Usage
		got       bool
	)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if len(data) == 0 {
			continue
		}
		if bytes.Equal(data, []byte("[DONE]")) {
			break
		}
		var c chunk
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("openai: stream: %w", err)
		}
		if c.Error != nil {
			return nil, fmt.Errorf("openai: %s", c.Error.text())
		}
		got = true
		if c.Usage != nil {
			usage.InputTokens = c.Usage.PromptTokens
			usage.OutputTokens = c.Usage.CompletionTokens
			if d := c.Usage.PromptTokensDetails; d != nil {
				usage.CacheReadTokens = d.CachedTokens
			}
		}
		for _, ch := range c.Choices {
			d := ch.Delta
			if d.Content != nil && *d.Content != "" {
				text.WriteString(*d.Content)
				emit(ai.Event{Type: ai.EventText, Text: *d.Content})
			}
			if r := d.ReasoningContent + d.Reasoning; r != "" {
				reasoning.WriteString(r)
				emit(ai.Event{Type: ai.EventThinking, Text: r})
			}
			for _, tc := range d.ToolCalls {
				idx := len(calls)
				if tc.Index != nil {
					idx = *tc.Index
				}
				var cur *pendingCall
				for _, pc := range calls {
					if pc.index == idx && (tc.ID == "" || pc.id == "" || pc.id == tc.ID) {
						cur = pc
						break
					}
				}
				if cur == nil {
					cur = &pendingCall{index: idx}
					calls = append(calls, cur)
				}
				if tc.ID != "" {
					cur.id = tc.ID
				}
				if tc.Function.Name != "" {
					cur.name = tc.Function.Name
				}
				cur.args.WriteString(tc.Function.Arguments)
			}
			if ch.FinishReason != nil && *ch.FinishReason != "" {
				finish = *ch.FinishReason
			}
		}
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("openai: stream: %w", err)
	}
	if !got {
		return nil, errors.New("openai: stream ended without a message")
	}
	t := p.turn(text.String(), reasoning.String(), calls, finish, usage)
	p.debug(fmt.Sprintf("openai: turn model=%s stop=%s calls=%d in=%d out=%d", model, t.StopReason, len(t.ToolCalls), usage.InputTokens, usage.OutputTokens))
	return t, nil
}

// turn projects the accumulated stream onto ai.Turn. A call whose
// arguments were cut short (finish_reason length) is stored and reported
// with input {} so the history stays valid; a call without an id gets one,
// since the loop and the tool messages correlate by it.
func (p *Provider) turn(text, reasoning string, calls []*pendingCall, finish string, usage ai.Usage) *ai.Turn {
	msg := chatMessage{Role: "assistant", Content: text, ReasoningContent: reasoning}
	t := &ai.Turn{Text: text, Usage: usage}
	for i, c := range calls {
		if c.name == "" {
			continue
		}
		id := c.id
		if id == "" {
			id = fmt.Sprintf("call_%d", i)
		}
		args := json.RawMessage(strings.TrimSpace(c.args.String()))
		if len(args) == 0 || !json.Valid(args) {
			args = json.RawMessage("{}")
		}
		msg.ToolCalls = append(msg.ToolCalls, toolCall{ID: id, Type: "function", Function: toolFunction{Name: c.name, Arguments: string(args)}})
		t.ToolCalls = append(t.ToolCalls, ai.ToolCall{ID: id, Name: c.name, Input: args})
	}
	switch finish {
	case "tool_calls":
		t.StopReason = ai.StopReasonToolUse
	case "length":
		t.StopReason = "max_tokens"
	case "content_filter":
		t.StopReason = "refusal"
	case "stop", "":
		t.StopReason = "end_turn"
	default:
		t.StopReason = finish
	}
	// Some servers finish with "stop" after a complete tool call.
	if t.StopReason == "end_turn" && len(t.ToolCalls) > 0 {
		t.StopReason = ai.StopReasonToolUse
	}
	t.Message = ai.Message{
		Role:      ai.RoleAssistant,
		Content:   mustMarshal([]chatMessage{msg}),
		Text:      text,
		ToolCalls: t.ToolCalls,
		CreatedAt: time.Now().UTC(),
	}
	return t
}

// request builds the body: the system prompt first, then the history's
// wire messages in order.
func (p *Provider) request(model string, in ai.TurnInput) ([]byte, error) {
	req := request{
		Model:           model,
		Stream:          true,
		StreamOptions:   streamOptions{IncludeUsage: true},
		MaxTokens:       in.MaxTokens,
		ReasoningEffort: p.effort,
	}
	if req.MaxTokens <= 0 {
		req.MaxTokens = p.maxTokens
	}
	if in.System != "" {
		req.Messages = append(req.Messages, mustMarshal(chatMessage{Role: "system", Content: in.System}))
	}
	for i, m := range in.Messages {
		var msgs []json.RawMessage
		if err := json.Unmarshal(m.Content, &msgs); err != nil {
			return nil, fmt.Errorf("openai: message %d content: %w", i, err)
		}
		req.Messages = append(req.Messages, msgs...)
	}
	for _, t := range in.Tools {
		schema := t.InputSchema()
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		if !json.Valid(schema) {
			return nil, fmt.Errorf("openai: tool %s schema is not valid JSON", t.Name())
		}
		req.Tools = append(req.Tools, requestTool{Type: "function", Function: requestFunction{
			Name: t.Name(), Description: t.Description(), Parameters: schema,
		}})
	}
	return json.Marshal(req)
}

// do sends a request and returns a 2xx response; any other status is read
// and returned as an *httpError.
func (p *Provider) do(ctx context.Context, method, url string, body []byte, accept string) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if p.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+p.bearer)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		resp.Body.Close()
		return nil, &httpError{status: resp.StatusCode, message: errorMessage(raw)}
	}
	return resp, nil
}

// httpError is a non-2xx answer.
type httpError struct {
	status  int
	message string
}

func (e *httpError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("openai: the API answered %d", e.status)
	}
	return fmt.Sprintf("openai: the API answered %d: %s", e.status, e.message)
}

// apiError is the error object of the dialect: {"error": {"message": ...}}
// or, from some servers, {"error": "..."}.
type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
	raw     string
}

func (e *apiError) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		e.raw = s
		return nil
	}
	type plain apiError
	var v plain
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*e = apiError(v)
	return nil
}

func (e *apiError) text() string {
	if e.Message != "" {
		return e.Message
	}
	return e.raw
}

// errorMessage extracts the message from an error body; an unparseable
// body is returned trimmed to one line.
func errorMessage(raw []byte) string {
	var body struct {
		Error *apiError `json:"error"`
	}
	if json.Unmarshal(raw, &body) == nil && body.Error != nil && body.Error.text() != "" {
		return body.Error.text()
	}
	s := strings.TrimSpace(string(raw))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		// Only this package's own structs reach here and they always marshal.
		panic(fmt.Sprintf("openai: marshal: %v", err))
	}
	return b
}

func (p *Provider) debug(msg string) {
	if p.log != nil {
		p.log.Debug(msg)
	}
}
