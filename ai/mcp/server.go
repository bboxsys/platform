// Package mcp serves ai.Tools to MCP clients over streamable HTTP and
// consumes remote MCP servers as ai.Tools, on the official Go MCP SDK.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/bboxsys/platform/ai"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// DefaultServerName is the implementation name Handler advertises.
	DefaultServerName = "bboxsys"
	// DefaultServerVersion is the implementation version Handler advertises.
	DefaultServerVersion = "1.0.0"
)

// NewServer returns an MCP server offering tools. Each tool's input schema
// is published as-is and its ReadOnly flag becomes the readOnlyHint
// annotation. A tool whose schema is not a JSON object is skipped, since
// the SDK refuses it. The server is not connected to any transport.
func NewServer(name, version string, tools []ai.Tool) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: name, Version: version}, nil)
	for _, t := range tools {
		schema, ok := objectSchema(t.InputSchema())
		if !ok {
			continue
		}
		s.AddTool(&sdk.Tool{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: schema,
			Annotations: &sdk.ToolAnnotations{ReadOnlyHint: t.ReadOnly()},
		}, toolHandler(t))
	}
	return s
}

// objectSchema decodes a tool schema into the map the SDK publishes. A
// schema without a type is taken as an object; any other type is refused.
func objectSchema(raw json.RawMessage) (map[string]any, bool) {
	m := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, false
		}
	}
	switch typ := m["type"]; typ {
	case nil:
		m["type"] = "object"
	case "object":
	default:
		return nil, false
	}
	if _, ok := m["properties"]; !ok {
		m["properties"] = map[string]any{}
	}
	return m, true
}

// toolHandler adapts an ai.Tool to the SDK's low-level tool handler. A
// failed call is reported to the client as an isError result, never as a
// protocol error, so the model can read it.
func toolHandler(t ai.Tool) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (res *sdk.CallToolResult, err error) {
		defer func() {
			if r := recover(); r != nil {
				res = errorResult(fmt.Sprintf("tool %s panicked: %v", t.Name(), r))
				err = nil
			}
		}()
		input := json.RawMessage("{}")
		if req != nil && req.Params != nil && req.Params.Arguments != nil {
			b, err := json.Marshal(req.Params.Arguments)
			if err != nil {
				return errorResult(fmt.Sprintf("tool %s: encode arguments: %v", t.Name(), err)), nil
			}
			input = b
		}
		r, err := t.Execute(ctx, input)
		if err != nil {
			return errorResult(fmt.Sprintf("tool %s: %v", t.Name(), err)), nil
		}
		out := &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{Text: r.Content}},
			IsError: r.IsError,
		}
		if sc := structured(r.JSON); sc != nil {
			out.StructuredContent = sc
		}
		return out, nil
	}
}

// structured returns raw as MCP structuredContent, which the protocol
// requires to be a JSON object: an object passes through, any other JSON
// value (a list endpoint's array, a scalar) is wrapped as {"result": v},
// and invalid JSON yields nil so only the text content is sent.
func structured(raw json.RawMessage) any {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return nil
	}
	if trimmed[0] == '{' {
		return json.RawMessage(trimmed)
	}
	return map[string]json.RawMessage{"result": json.RawMessage(trimmed)}
}

func errorResult(msg string) *sdk.CallToolResult {
	return &sdk.CallToolResult{
		Content: []sdk.Content{&sdk.TextContent{Text: msg}},
		IsError: true,
	}
}

// Handler serves MCP over streamable HTTP with DefaultServerName and
// DefaultServerVersion. See HandlerWithInfo.
func Handler(get func(r *http.Request) ([]ai.Tool, error)) http.Handler {
	return HandlerWithInfo(DefaultServerName, DefaultServerVersion, get)
}

// HandlerWithInfo serves MCP over streamable HTTP. get builds the tool set
// for each request from whatever the route put in the request (the caller
// identity, for example); an error answers 400 and serves nothing.
//
// The handler is stateless (StreamableHTTPOptions.Stateless): every POST
// creates a short-lived session over its own request, no Mcp-Session-Id
// is issued, and GET and DELETE answer 405, so instances behind a load
// balancer need no session affinity. The SDK connects that session with
// the request's context, and jsonrpc2 derives every tool handler's
// context from it with values preserved, so ai.CallerFromContext works
// inside Execute; the tools are also wrapped with the caller from the
// request so this does not depend on the SDK's internals. Localhost DNS
// rebinding protection is off because the endpoint sits behind bearer
// authentication and is normally reached through a reverse proxy whose
// Host header is not localhost.
func HandlerWithInfo(name, version string, get func(r *http.Request) ([]ai.Tool, error)) http.Handler {
	getServer := func(r *http.Request) *sdk.Server {
		tools, err := get(r)
		if err != nil {
			return nil
		}
		if caller, ok := ai.CallerFromContext(r.Context()); ok {
			wrapped := make([]ai.Tool, len(tools))
			for i, t := range tools {
				wrapped[i] = &callerTool{Tool: t, caller: caller}
			}
			tools = wrapped
		}
		return NewServer(name, version, tools)
	}
	return sdk.NewStreamableHTTPHandler(getServer, &sdk.StreamableHTTPOptions{
		Stateless:                  true,
		DisableLocalhostProtection: true,
	})
}

// callerTool runs a tool with a fixed caller in context.
type callerTool struct {
	ai.Tool
	caller ai.Caller
}

func (c *callerTool) Execute(ctx context.Context, input json.RawMessage) (ai.Result, error) {
	if _, ok := ai.CallerFromContext(ctx); !ok {
		ctx = ai.WithCaller(ctx, c.caller)
	}
	return c.Tool.Execute(ctx, input)
}
