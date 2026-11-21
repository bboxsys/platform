package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bboxsys/platform/ai"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// ClientName is the implementation name Connect reports to servers.
	ClientName = "bboxsys-ai"
	// DefaultHTTPTimeout bounds each HTTP exchange with a remote server
	// when ClientOptions.HTTPTimeout is zero.
	DefaultHTTPTimeout = 30 * time.Second
)

// ClientOptions configures ConnectWithOptions.
type ClientOptions struct {
	// TrustReadOnly takes the server's readOnlyHint annotations at their
	// word. Off by default: a remote server decides its own annotations,
	// so a write tool could claim to be read-only and be auto-approved.
	// Untrusted, every remote tool is a write and needs approval. Turn it
	// on only for a server the operator controls.
	TrustReadOnly bool
	// HTTPTimeout bounds each HTTP exchange. Default DefaultHTTPTimeout.
	HTTPTimeout time.Duration
}

// Client is a session with one remote MCP server.
type Client struct {
	name          string
	session       *sdk.ClientSession
	trustReadOnly bool
}

// Connect opens a streamable HTTP session with the server at endpoint,
// sending bearer on every request, with the default options: readOnlyHint
// is not trusted, so every remote tool is a write. See ConnectWithOptions.
func Connect(ctx context.Context, name, endpoint, bearer string) (*Client, error) {
	return ConnectWithOptions(ctx, name, endpoint, bearer, ClientOptions{})
}

// ConnectWithOptions opens a streamable HTTP session with the server at
// endpoint, sending bearer on every request. name prefixes the wrapped
// tool names ("{name}_") so several servers can be offered together; it is
// sanitized to the tool name alphabet. Only request and response traffic
// is used: no standalone SSE stream is opened, so servers that answer GET
// with 405 work. ctx bounds the handshake; each HTTP exchange is also
// bounded by opts.HTTPTimeout.
func ConnectWithOptions(ctx context.Context, name, endpoint, bearer string, opts ClientOptions) (*Client, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("mcp: endpoint is required")
	}
	timeout := opts.HTTPTimeout
	if timeout <= 0 {
		timeout = DefaultHTTPTimeout
	}
	transport := &sdk.StreamableClientTransport{
		Endpoint: endpoint,
		HTTPClient: &http.Client{
			Timeout:   timeout,
			Transport: &bearerTransport{bearer: bearer, base: http.DefaultTransport},
		},
		DisableStandaloneSSE: true,
	}
	client := sdk.NewClient(&sdk.Implementation{Name: ClientName, Version: DefaultServerVersion}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp: connect %s: %w", endpoint, err)
	}
	c := newClient(name, session)
	c.trustReadOnly = opts.TrustReadOnly
	return c, nil
}

// newClient wraps an open session.
func newClient(name string, session *sdk.ClientSession) *Client {
	return &Client{name: sanitizeName(name), session: session}
}

// Name returns the sanitized server name used as the tool prefix.
func (c *Client) Name() string { return c.name }

// Tools lists the server's tools and wraps each as an ai.Tool. A tool is
// read-only only when the client trusts the server's annotations
// (ClientOptions.TrustReadOnly) and the server sets readOnlyHint.
func (c *Client) Tools(ctx context.Context) ([]ai.Tool, error) {
	var tools []ai.Tool
	seen := map[string]bool{}
	cursor := ""
	for {
		res, err := c.session.ListTools(ctx, &sdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("mcp: list tools on %s: %w", c.name, err)
		}
		for _, t := range res.Tools {
			if t == nil || t.Name == "" {
				continue
			}
			name := uniqueName(c.name+"_"+sanitizeName(t.Name), seen)
			schema := json.RawMessage(`{"type":"object","properties":{}}`)
			if t.InputSchema != nil {
				if b, err := json.Marshal(t.InputSchema); err == nil && string(b) != "null" {
					schema = b
				}
			}
			readOnly := c.trustReadOnly && t.Annotations != nil && t.Annotations.ReadOnlyHint
			remote := t.Name
			tools = append(tools, ai.NewTool(name, t.Description, schema, readOnly,
				func(ctx context.Context, input json.RawMessage) (ai.Result, error) {
					return c.call(ctx, remote, input)
				}))
		}
		if res.NextCursor == "" {
			return tools, nil
		}
		cursor = res.NextCursor
	}
}

// call runs one remote tool.
func (c *Client) call(ctx context.Context, name string, input json.RawMessage) (ai.Result, error) {
	args := map[string]any{}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return ai.ErrorResult("invalid input for %s: %v", name, err), nil
		}
	}
	res, err := c.session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return ai.Result{}, fmt.Errorf("mcp: call %s on %s: %w", name, c.name, err)
	}
	out := ai.Result{IsError: res.IsError}
	var parts []string
	for _, content := range res.Content {
		switch v := content.(type) {
		case *sdk.TextContent:
			parts = append(parts, v.Text)
		case *sdk.ImageContent:
			parts = append(parts, "[image "+v.MIMEType+" omitted]")
		case *sdk.AudioContent:
			parts = append(parts, "[audio "+v.MIMEType+" omitted]")
		case *sdk.EmbeddedResource:
			if v.Resource != nil && v.Resource.Text != "" {
				parts = append(parts, v.Resource.Text)
			} else if v.Resource != nil {
				parts = append(parts, "[resource "+v.Resource.URI+" omitted]")
			}
		case *sdk.ResourceLink:
			parts = append(parts, "[resource link "+v.URI+"]")
		}
	}
	out.Content = strings.Join(parts, "\n")
	if res.StructuredContent != nil {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			out.JSON = b
		}
	}
	return out, nil
}

// Close ends the session.
func (c *Client) Close() error {
	if c.session == nil {
		return nil
	}
	if err := c.session.Close(); err != nil {
		return fmt.Errorf("mcp: close %s: %w", c.name, err)
	}
	return nil
}

// bearerTransport adds the Authorization header to every request.
type bearerTransport struct {
	bearer string
	base   http.RoundTripper
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.bearer != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+t.bearer)
	}
	return t.base.RoundTrip(req)
}

// sanitizeName keeps s within ai.ToolNamePattern's alphabet, replacing
// runs of other characters with one underscore.
func sanitizeName(s string) string {
	var b strings.Builder
	underscore := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-'
		switch {
		case ok:
			b.WriteRune(r)
			underscore = false
		case underscore:
		default:
			b.WriteByte('_')
			underscore = true
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "tool"
	}
	return out
}

// uniqueName cuts name to the pattern's length and adds a counter on a
// repeat.
func uniqueName(name string, seen map[string]bool) string {
	if len(name) > 64 {
		name = strings.TrimRight(name[:64], "_")
	}
	candidate := name
	for i := 2; seen[candidate]; i++ {
		suffix := fmt.Sprintf("_%d", i)
		base := name
		if len(base)+len(suffix) > 64 {
			base = base[:64-len(suffix)]
		}
		candidate = base + suffix
	}
	seen[candidate] = true
	return candidate
}
