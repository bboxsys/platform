// Package apitool turns a service's OpenAPI document into ai.Tools that
// call the service's own http.Handler.
//
// Every call is a synthetic *http.Request dispatched through the handler
// with the caller's bearer and account headers, so the ID middleware and
// every authorization wrapper run unchanged: the handlers are the policy
// and there is no tool-only path to a record. This is the natsapi rule
// applied to the agent.
package apitool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strings"

	"github.com/bboxsys/platform/ai"
)

const (
	// HeaderTransport is set on every synthetic request so handlers and
	// logs can tell an agent call from a direct one.
	HeaderTransport = "X-Transport"
	// TransportAI is the value of HeaderTransport.
	TransportAI = "ai"
	// HeaderAccountID carries the caller's account to the ID middleware.
	HeaderAccountID = "X-Blackbox-AccountID"
	// HeaderUserID carries the caller's user id to the ID middleware.
	HeaderUserID = "X-Blackbox-UserID"

	// DefaultBaseURL is the host of the synthetic request when
	// Options.BaseURL is empty. The handler never dials it.
	DefaultBaseURL = "http://localhost"
	// DefaultMaxBodyBytes bounds a response body returned to the model.
	DefaultMaxBodyBytes = 1 << 20
)

// Options configures the generated tools.
type Options struct {
	// Prefix is prepended to every tool name, for example "admin_".
	Prefix string
	// Include filters operations by method (upper case) and path template;
	// nil includes every operation. The generic request tool also calls it,
	// with the concrete request path instead of a template, and refuses a
	// call it rejects, so a filter that must hold for both matches a
	// template segment ("{id}") and a concrete one alike.
	Include func(method, path string) bool
	// ReadOnly overrides the read-only decision for one operation (method
	// upper case, path template). When it returns ok, readOnly is used as
	// is; otherwise the operation's x-ai-read-only extension decides when
	// present, and then the method (GET and HEAD are read-only). Use it, or
	// x-ai-read-only: false in the document, for a GET with side effects.
	ReadOnly func(method, path string) (readOnly bool, ok bool)
	// BaseURL is the scheme and host of the synthetic request. Default
	// DefaultBaseURL.
	BaseURL string
	// MaxBodyBytes truncates a response body returned to the model. Default
	// DefaultMaxBodyBytes.
	MaxBodyBytes int
}

func (o Options) withDefaults() Options {
	if o.BaseURL == "" {
		o.BaseURL = DefaultBaseURL
	}
	if o.MaxBodyBytes <= 0 {
		o.MaxBodyBytes = DefaultMaxBodyBytes
	}
	o.BaseURL = strings.TrimRight(o.BaseURL, "/")
	return o
}

// Operation describes one generated tool, for a status route that lists
// what the document produced.
type Operation struct {
	Method   string `json:"method"`
	Path     string `json:"path"`
	Name     string `json:"name"`
	ReadOnly bool   `json:"read_only"`
}

// Operations lists the operations FromOpenAPI would generate for spec
// without building the tools.
func Operations(spec []byte, o Options) ([]Operation, error) {
	ops, err := parse(spec, o)
	if err != nil {
		return nil, err
	}
	out := make([]Operation, 0, len(ops))
	for _, op := range ops {
		out = append(out, Operation{Method: op.method, Path: op.path, Name: op.name, ReadOnly: op.readOnly})
	}
	return out, nil
}

// FromOpenAPI builds one tool per operation in spec, an OpenAPI 3 document,
// executing each through h. Operations whose request body is not JSON
// (multipart uploads, for example) are skipped. Tool names are unique and
// match ai.ToolNamePattern.
func FromOpenAPI(spec []byte, h http.Handler, o Options) ([]ai.Tool, error) {
	if h == nil {
		return nil, fmt.Errorf("apitool: handler is required")
	}
	o = o.withDefaults()
	ops, err := parse(spec, o)
	if err != nil {
		return nil, err
	}
	tools := make([]ai.Tool, 0, len(ops))
	for _, op := range ops {
		if err := checkName(op.name); err != nil {
			return nil, fmt.Errorf("apitool: %s %s: %w", op.method, op.path, err)
		}
		op := op
		exec := &executor{h: h, o: o}
		tools = append(tools, ai.NewTool(op.name, op.description, op.schema, op.readOnly,
			func(ctx context.Context, input json.RawMessage) (ai.Result, error) {
				return exec.runOperation(ctx, op, input)
			}))
	}
	return tools, nil
}

// requestToolSchema is the input of the generic request tool.
const requestToolSchema = `{
  "type": "object",
  "properties": {
    "method": {"type": "string", "description": "HTTP method", "enum": ["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"]},
    "path": {"type": "string", "description": "Request path starting with /, for example /api/v1/users"},
    "query": {"type": "object", "description": "Query parameters as name to value; an array value repeats the parameter", "additionalProperties": true},
    "body": {"description": "JSON request body for POST, PUT, and PATCH"}
  },
  "required": ["method", "path"]
}`

// NewRequestTool returns a tool named Prefix+"api_request" that calls a
// route of h, for routes the document does not list. It is never read-only,
// whatever the method and whatever Options.ReadOnly says: the approval
// policy decides whether a call runs. It only reaches paths under /api/,
// given in canonical form (no "." or ".." segments, no repeated slashes),
// and refuses any method and path Options.Include rejects, so a route
// excluded from the generated tools cannot be reached through it.
func NewRequestTool(h http.Handler, o Options) ai.Tool {
	o = o.withDefaults()
	exec := &executor{h: h, o: o}
	return ai.NewTool(o.Prefix+"api_request",
		"Call any route of the HTTP API with the caller's own identity. Prefer a specific tool when one exists; use this for routes that have none.\nInput: method, path, optional query object and JSON body.",
		json.RawMessage(requestToolSchema), false,
		func(ctx context.Context, input json.RawMessage) (ai.Result, error) {
			return exec.runRequest(ctx, input)
		})
}

// executor dispatches synthetic requests through a handler.
type executor struct {
	h http.Handler
	o Options
}

// runOperation executes one generated operation.
func (e *executor) runOperation(ctx context.Context, op operation, input json.RawMessage) (ai.Result, error) {
	args, err := decodeInput(input)
	if err != nil {
		return ai.ErrorResult("invalid input: %v", err), nil
	}
	path := op.path
	for _, name := range op.pathParams {
		raw, ok := args[name]
		if !ok || isNull(raw) {
			return ai.ErrorResult("missing path parameter %q", name), nil
		}
		path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(scalarString(raw)))
	}
	query := url.Values{}
	for _, name := range op.queryParams {
		raw, ok := args[name]
		if !ok || isNull(raw) {
			continue
		}
		addQuery(query, name, raw)
	}
	var body []byte
	if op.hasBody {
		if raw, ok := args["body"]; ok && !isNull(raw) {
			body = compact(raw)
		}
	}
	return e.do(ctx, op.method, path, query, body)
}

// requestInput is the generic tool's input.
type requestInput struct {
	Method string                     `json:"method"`
	Path   string                     `json:"path"`
	Query  map[string]json.RawMessage `json:"query"`
	Body   json.RawMessage            `json:"body"`
}

// runRequest executes the generic request tool.
func (e *executor) runRequest(ctx context.Context, input json.RawMessage) (ai.Result, error) {
	var in requestInput
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return ai.ErrorResult("invalid input: %v", err), nil
		}
	}
	method := strings.ToUpper(strings.TrimSpace(in.Method))
	if method == "" {
		return ai.ErrorResult("method is required"), nil
	}
	if !strings.HasPrefix(in.Path, "/") {
		return ai.ErrorResult("path must start with /"), nil
	}
	u, err := url.ParseRequestURI(in.Path)
	if err != nil {
		return ai.ErrorResult("invalid path: %v", err), nil
	}
	if !strings.HasPrefix(u.Path, "/api/") {
		return ai.ErrorResult("path must start with /api/"), nil
	}
	if canonicalPath(u.Path) != u.Path {
		return ai.ErrorResult("path must be canonical (no dot segments or repeated slashes)"), nil
	}
	if e.o.Include != nil && !e.o.Include(method, u.Path) {
		return ai.ErrorResult("%s %s is not available to the assistant", method, u.Path), nil
	}
	query := u.Query()
	for name, raw := range in.Query {
		if isNull(raw) {
			continue
		}
		addQuery(query, name, raw)
	}
	var body []byte
	if !isNull(in.Body) {
		body = compact(in.Body)
	}
	// The escaped form is dispatched so the handler sees exactly u.Path,
	// the path Include checked.
	return e.do(ctx, method, u.EscapedPath(), query, body)
}

// canonicalPath cleans p, keeping a trailing slash.
func canonicalPath(p string) string {
	c := path.Clean(p)
	if strings.HasSuffix(p, "/") && c != "/" {
		c += "/"
	}
	return c
}

// do builds the request from the caller in ctx, serves it, and renders
// the response for the model.
func (e *executor) do(ctx context.Context, method, path string, query url.Values, body []byte) (ai.Result, error) {
	caller, ok := ai.CallerFromContext(ctx)
	if !ok {
		return ai.ErrorResult("no caller identity in context; the request was not sent"), nil
	}
	target := e.o.BaseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return ai.ErrorResult("build request: %v", err), nil
	}
	req.RequestURI = req.URL.RequestURI()
	req.RemoteAddr = TransportAI + ":" + caller.AccountID
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.ContentLength = int64(len(body))
	}
	if caller.Bearer != "" {
		req.Header.Set("Authorization", "Bearer "+caller.Bearer)
	}
	if caller.AccountID != "" {
		req.Header.Set(HeaderAccountID, caller.AccountID)
	}
	if caller.UserID != "" {
		req.Header.Set(HeaderUserID, caller.UserID)
	}
	req.Header.Set(HeaderTransport, TransportAI)

	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return render(rec.Code, rec.Body.Bytes(), e.o.MaxBodyBytes), nil
}

// render turns a response into a Result: pretty JSON when the body parses,
// text otherwise, prefixed with the status on anything but 2xx.
func render(status int, body []byte, limit int) ai.Result {
	truncated := false
	if len(body) > limit {
		body = body[:limit]
		truncated = true
	}
	res := ai.Result{IsError: status >= 400}
	var content string
	if !truncated && json.Valid(body) {
		var buf bytes.Buffer
		if err := json.Indent(&buf, body, "", "  "); err == nil {
			content = buf.String()
			res.JSON = json.RawMessage(compact(body))
		}
	}
	if content == "" {
		content = string(body)
	}
	if truncated {
		content += fmt.Sprintf("\n[truncated at %d bytes]", limit)
	}
	if status < 200 || status > 299 {
		line := fmt.Sprintf("HTTP %d %s", status, http.StatusText(status))
		if strings.TrimSpace(content) == "" {
			content = line
		} else {
			content = line + "\n" + content
		}
	}
	res.Content = content
	return res
}

// decodeInput reads the tool input as a property map. Empty input is an
// empty object.
func decodeInput(input json.RawMessage) (map[string]json.RawMessage, error) {
	args := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(input)) == 0 {
		return args, nil
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return nil, err
	}
	return args, nil
}

func isNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

// scalarString renders a JSON scalar as a parameter value: a string
// unquoted, anything else as its JSON text.
func scalarString(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(compact(raw))
}

// addQuery adds one parameter; an array value repeats it.
func addQuery(q url.Values, name string, raw json.RawMessage) {
	var items []json.RawMessage
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		if err := json.Unmarshal(raw, &items); err == nil {
			for _, item := range items {
				if !isNull(item) {
					q.Add(name, scalarString(item))
				}
			}
			return
		}
	}
	q.Add(name, scalarString(raw))
}

// compact returns raw without insignificant whitespace, or raw itself when
// it is not valid JSON.
func compact(raw json.RawMessage) []byte {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return raw
	}
	return buf.Bytes()
}
