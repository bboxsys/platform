// Package anthropic is the ai.Provider on the official Anthropic SDK. It
// runs one streamed Messages request per turn, stores the response content
// blocks verbatim as the session's native history, and enables server-side
// compaction so the harness never has to rewrite earlier turns.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"

	"github.com/bboxsys/platform/ai"
	"github.com/bboxsys/platform/logger"
)

const (
	// DefaultModel is used when the credential names none.
	DefaultModel = "claude-opus-5-5"
	// DefaultMaxTokens is the output cap when neither the options nor the
	// credential set one.
	DefaultMaxTokens = 16000
	// DefaultEffort is the reasoning effort when neither the options nor
	// the credential set one.
	DefaultEffort = "medium"
	// MemoryToolName is the tool name sent as the native memory tool.
	MemoryToolName = "memory"
)

// Options tunes the provider. The zero value is the recommended setup:
// medium effort, 16000 output tokens, compaction on, fallbacks off.
type Options struct {
	// Effort is the reasoning effort (low, medium, high, xhigh, max). It
	// overrides the credential's ai_effort tag; empty means that tag or
	// DefaultEffort.
	Effort string
	// MaxTokens caps output tokens per turn; zero means the credential's
	// ai_max_output tag or DefaultMaxTokens.
	MaxTokens int
	// Fallbacks enables server-side refusal fallbacks (fallbacks: "default").
	Fallbacks bool
	// DisableCompaction turns off server-side compaction, which is on by
	// default. Without it a long session eventually overflows the window.
	DisableCompaction bool
	// HTTPClient replaces the SDK's default transport, for tests and proxies.
	HTTPClient *http.Client
	// Logger receives debug lines; nil is silent.
	Logger *logger.Logger
}

// Provider is an ai.Provider for one credential.
type Provider struct {
	client    anthropic.Client
	model     string
	effort    string
	maxTokens int
	opts      Options
}

// New builds a provider from a resolved model credential. The credential's
// Bearer is the API key, Endpoint (when set) the base URL, and Model the
// model id.
func New(cred ai.Credential, opts Options) (*Provider, error) {
	if cred.Bearer == "" {
		return nil, errors.New("anthropic: credential has no bearer")
	}
	ro := []option.RequestOption{option.WithAPIKey(cred.Bearer)}
	if cred.Endpoint != "" {
		ro = append(ro, option.WithBaseURL(cred.Endpoint))
	}
	if opts.HTTPClient != nil {
		ro = append(ro, option.WithHTTPClient(opts.HTTPClient))
	}
	p := &Provider{
		client: anthropic.NewClient(ro...),
		model:  cred.Model,
		opts:   opts,
	}
	if p.model == "" {
		p.model = DefaultModel
	}
	p.effort = opts.Effort
	if p.effort == "" {
		p.effort = cred.Tag(ai.TagEffort)
	}
	if p.effort == "" {
		p.effort = DefaultEffort
	}
	p.maxTokens = opts.MaxTokens
	if p.maxTokens <= 0 {
		p.maxTokens = cred.IntTag(ai.TagMaxOutput, DefaultMaxTokens)
	}
	return p, nil
}

// Factory returns an ai.ProviderFactory that builds providers with opts.
func Factory(opts Options) ai.ProviderFactory {
	return func(cred ai.Credential) (ai.Provider, error) {
		return New(cred, opts)
	}
}

// Name returns "anthropic".
func (p *Provider) Name() string { return ai.ProviderAnthropic }

// Model returns the model id in use.
func (p *Provider) Model() string { return p.model }

// UserMessage builds a user message holding one text block.
func (p *Provider) UserMessage(text string) ai.Message {
	content := mustMarshal([]anthropic.BetaContentBlockParamUnion{anthropic.NewBetaTextBlock(text)})
	return ai.Message{Role: ai.RoleUser, Content: content, Text: text, CreatedAt: time.Now().UTC()}
}

// ToolResultMessage builds the user message carrying one tool_result block
// per result, in order. A denied or failed call is marked is_error so the
// model treats the text as a failure to recover from.
func (p *Provider) ToolResultMessage(results []ai.ToolResult) ai.Message {
	blocks := make([]anthropic.BetaContentBlockParamUnion, 0, len(results))
	for _, r := range results {
		text := r.Result.Content
		if text == "" {
			// The API rejects an empty text block.
			text = "(no output)"
		}
		blocks = append(blocks, anthropic.NewBetaToolResultBlock(r.ID, text, r.Result.IsError || r.Denied))
	}
	return ai.Message{
		Role:        ai.RoleUser,
		Content:     mustMarshal(blocks),
		ToolResults: ai.DisplayToolResults(results),
		CreatedAt:   time.Now().UTC(),
	}
}

// Turn streams one Messages request, emitting text, thinking, and
// compaction events as they arrive, and returns the accumulated assistant
// message with its content blocks stored verbatim.
func (p *Provider) Turn(ctx context.Context, in ai.TurnInput, emit func(ai.Event)) (*ai.Turn, error) {
	if emit == nil {
		emit = func(ai.Event) {}
	}
	params, err := p.params(in)
	if err != nil {
		return nil, err
	}

	stream := p.client.Beta.Messages.NewStreaming(ctx, params)
	defer stream.Close()
	var msg anthropic.BetaMessage
	for stream.Next() {
		ev := stream.Current()
		if err := msg.Accumulate(ev); err != nil {
			return nil, fmt.Errorf("anthropic: accumulate %s: %w", ev.Type, err)
		}
		switch ev.Type {
		case "content_block_delta":
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text != "" {
					emit(ai.Event{Type: ai.EventText, Text: ev.Delta.Text})
				}
			case "thinking_delta":
				if ev.Delta.Thinking != "" {
					emit(ai.Event{Type: ai.EventThinking, Text: ev.Delta.Thinking})
				}
			}
		case "content_block_stop":
			// The compaction summary is complete once its block stops; the
			// block itself stays in the history for the next request.
			if i := int(ev.Index); i < len(msg.Content) && msg.Content[i].Type == "compaction" {
				emit(ai.Event{Type: ai.EventCompaction, Text: msg.Content[i].Content.OfString})
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, fmt.Errorf("anthropic: stream: %w", err)
	}
	if msg.ID == "" && len(msg.Content) == 0 {
		return nil, errors.New("anthropic: stream ended without a message")
	}
	return p.turn(msg)
}

// turn projects the accumulated message onto ai.Turn.
//
// Two shapes a cut-off turn can take must still produce a history the API
// accepts on the next request, because the loop appends the message
// verbatim and never rewrites it: a tool_use block whose input JSON was
// cut short (max_tokens mid-input) is stored and reported with input {},
// and a message with no content blocks at all (a refusal can end with [])
// is stored as one text block naming the stop reason, while Turn.Text
// stays empty for display.
func (p *Provider) turn(msg anthropic.BetaMessage) (*ai.Turn, error) {
	t := &ai.Turn{
		StopReason: string(msg.StopReason),
		Usage: ai.Usage{
			InputTokens:      msg.Usage.InputTokens,
			OutputTokens:     msg.Usage.OutputTokens,
			CacheReadTokens:  msg.Usage.CacheReadInputTokens,
			CacheWriteTokens: msg.Usage.CacheCreationInputTokens,
		},
	}
	if t.StopReason == "" {
		t.StopReason = string(anthropic.BetaStopReasonEndTurn)
	}
	params := msg.ToParam().Content
	var text strings.Builder
	for i, block := range msg.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			input := json.RawMessage(block.Input)
			if len(input) == 0 || !json.Valid(input) {
				input = json.RawMessage("{}")
			}
			if params[i].OfToolUse != nil {
				params[i].OfToolUse.Input = input
			}
			t.ToolCalls = append(t.ToolCalls, ai.ToolCall{ID: block.ID, Name: block.Name, Input: input})
		}
	}
	if len(params) == 0 {
		params = []anthropic.BetaContentBlockParamUnion{
			anthropic.NewBetaTextBlock("(no content: stop reason " + t.StopReason + ")"),
		}
	}
	content, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal content: %w", err)
	}
	t.Text = text.String()
	t.Message = ai.Message{
		Role:      ai.RoleAssistant,
		Content:   content,
		Text:      t.Text,
		ToolCalls: t.ToolCalls,
		CreatedAt: time.Now().UTC(),
	}
	return t, nil
}

// params builds the request. The system prompt carries the cache
// breakpoint so tools and system are cached together across turns.
func (p *Provider) params(in ai.TurnInput) (anthropic.BetaMessageNewParams, error) {
	model := in.Model
	if model == "" {
		model = p.model
	}
	maxTokens := in.MaxTokens
	if maxTokens <= 0 {
		maxTokens = p.maxTokens
	}
	messages, err := history(in.Messages)
	if err != nil {
		return anthropic.BetaMessageNewParams{}, err
	}
	tools, err := tools(in.Tools)
	if err != nil {
		return anthropic.BetaMessageNewParams{}, err
	}

	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: int64(maxTokens),
		Messages:  messages,
		Tools:     tools,
		Thinking: anthropic.BetaThinkingConfigParamUnion{
			OfAdaptive: &anthropic.BetaThinkingConfigAdaptiveParam{
				Display: anthropic.BetaThinkingConfigAdaptiveDisplaySummarized,
			},
		},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffort(p.effort),
		},
	}
	if in.System != "" {
		params.System = []anthropic.BetaTextBlockParam{{
			Text:         in.System,
			CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
		}}
	}
	if !p.opts.DisableCompaction {
		params.Betas = append(params.Betas, anthropic.AnthropicBetaCompact2026_01_12)
		params.ContextManagement = anthropic.BetaContextManagementConfigParam{
			Edits: []anthropic.BetaContextManagementConfigEditUnionParam{
				{OfCompact20260112: &anthropic.BetaCompact20260112EditParam{}},
			},
		}
	}
	if p.opts.Fallbacks {
		// The string form routes by refusal category server-side, so no
		// model list is maintained here.
		params.Betas = append(params.Betas, anthropic.AnthropicBetaServerSideFallback2026_07_01)
		params.Fallbacks = anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
	}
	return params, nil
}

// history decodes the session's native content back into request messages.
func history(msgs []ai.Message) ([]anthropic.BetaMessageParam, error) {
	out := make([]anthropic.BetaMessageParam, 0, len(msgs))
	for i, m := range msgs {
		var content []anthropic.BetaContentBlockParamUnion
		if err := json.Unmarshal(m.Content, &content); err != nil {
			return nil, fmt.Errorf("anthropic: message %d content: %w", i, err)
		}
		out = append(out, anthropic.BetaMessageParam{
			Role:    anthropic.BetaMessageParamRole(m.Role),
			Content: content,
		})
	}
	return out, nil
}

// tools converts the tool set. A tool named "memory" is sent as the native
// memory tool, whose schema and prompting the model already knows.
func tools(ts []ai.Tool) ([]anthropic.BetaToolUnionParam, error) {
	if len(ts) == 0 {
		return nil, nil
	}
	out := make([]anthropic.BetaToolUnionParam, 0, len(ts))
	for _, t := range ts {
		if t.Name() == MemoryToolName {
			out = append(out, anthropic.BetaToolUnionParam{OfMemoryTool20250818: &anthropic.BetaMemoryTool20250818Param{}})
			continue
		}
		var schema anthropic.BetaToolInputSchemaParam
		raw := t.InputSchema()
		if len(raw) == 0 {
			raw = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		if err := schema.UnmarshalJSON(raw); err != nil {
			return nil, fmt.Errorf("anthropic: tool %s schema: %w", t.Name(), err)
		}
		tool := anthropic.BetaToolParam{
			Name:        t.Name(),
			InputSchema: schema,
		}
		if d := t.Description(); d != "" {
			tool.Description = anthropic.String(d)
		}
		out = append(out, anthropic.BetaToolUnionParam{OfTool: &tool})
	}
	return out, nil
}

func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		// Only SDK param structs reach here and they always marshal.
		panic(fmt.Sprintf("anthropic: marshal: %v", err))
	}
	return b
}

var _ ai.Checker = (*Provider)(nil)

// Check verifies the key, the endpoint, and the model by reading the model
// from the Models API. It sends no message, so it spends no tokens.
func (p *Provider) Check(ctx context.Context) (ai.CheckResult, error) {
	info, err := p.client.Models.Get(ctx, p.model, anthropic.ModelGetParams{})
	if err != nil {
		return ai.CheckResult{}, classifyCheck(err, p.model)
	}
	return ai.CheckResult{
		Model:         info.ID,
		DisplayName:   info.DisplayName,
		ContextWindow: info.MaxInputTokens,
		MaxOutput:     info.MaxTokens,
	}, nil
}

// classifyCheck turns an SDK error into an ai.CheckError with a message a
// person can act on. The SDK's own message carries the request URL and the
// response body; only the API's error message is kept.
func classifyCheck(err error, model string) error {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		return &ai.CheckError{Kind: ai.CheckUnreachable, Message: "the endpoint could not be reached: " + err.Error()}
	}
	detail := apiErrorMessage(apiErr)
	switch apiErr.StatusCode {
	case http.StatusUnauthorized:
		return &ai.CheckError{Kind: ai.CheckAuth, Message: "the API key was rejected" + suffix(detail)}
	case http.StatusForbidden:
		return &ai.CheckError{Kind: ai.CheckAuth, Message: "the API key is not allowed to use this API" + suffix(detail)}
	case http.StatusNotFound:
		return &ai.CheckError{Kind: ai.CheckNotFound, Message: fmt.Sprintf("model %s was not found", model) + suffix(detail)}
	default:
		return &ai.CheckError{Kind: ai.CheckRejected, Message: fmt.Sprintf("the API answered %d", apiErr.StatusCode) + suffix(detail)}
	}
}

// apiErrorMessage extracts error.message from the response body the SDK
// appends to its error text.
func apiErrorMessage(e *anthropic.Error) string {
	text := e.Error()
	i := strings.Index(text, "{")
	if i < 0 {
		return ""
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(text[i:]), &body) != nil {
		return ""
	}
	return body.Error.Message
}

func suffix(detail string) string {
	if detail == "" {
		return ""
	}
	return ": " + detail
}
