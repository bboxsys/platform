package apitool

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/bboxsys/platform/ai"
)

// document is the subset of an OpenAPI 3 document the generator reads.
type document struct {
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Components struct {
		Schemas       map[string]json.RawMessage `json:"schemas"`
		Parameters    map[string]json.RawMessage `json:"parameters"`
		RequestBodies map[string]json.RawMessage `json:"requestBodies"`
	} `json:"components"`
}

// operationDoc is one method entry under a path.
type operationDoc struct {
	OperationID string            `json:"operationId"`
	Summary     string            `json:"summary"`
	Description string            `json:"description"`
	Tags        []string          `json:"tags"`
	Parameters  []json.RawMessage `json:"parameters"`
	RequestBody json.RawMessage   `json:"requestBody"`
	// AIReadOnly is the x-ai-read-only extension: false marks an operation
	// with side effects as a write whatever its method.
	AIReadOnly *bool `json:"x-ai-read-only"`
}

type parameterDoc struct {
	Ref         string          `json:"$ref"`
	Name        string          `json:"name"`
	In          string          `json:"in"`
	Required    bool            `json:"required"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

type requestBodyDoc struct {
	Ref      string `json:"$ref"`
	Required bool   `json:"required"`
	Content  map[string]struct {
		Schema json.RawMessage `json:"schema"`
	} `json:"content"`
}

// operation is one generated operation: everything a tool needs.
type operation struct {
	name        string
	method      string
	path        string
	description string
	schema      json.RawMessage
	pathParams  []string
	queryParams []string
	hasBody     bool
	readOnly    bool
}

// httpMethods are the OpenAPI path item keys that are operations.
var httpMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// parse reads the document and returns the included operations in a stable
// order (path, then method), named and deduplicated.
func parse(spec []byte, o Options) ([]operation, error) {
	var doc document
	if err := json.Unmarshal(spec, &doc); err != nil {
		return nil, fmt.Errorf("apitool: parse OpenAPI document: %w", err)
	}
	if len(doc.Paths) == 0 {
		return nil, fmt.Errorf("apitool: OpenAPI document has no paths")
	}
	r := &resolver{doc: &doc}

	paths := make([]string, 0, len(doc.Paths))
	for p := range doc.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var ops []operation
	seen := make(map[string]int)
	for _, p := range paths {
		item := doc.Paths[p]
		var shared []json.RawMessage
		if raw, ok := item["parameters"]; ok {
			if err := json.Unmarshal(raw, &shared); err != nil {
				return nil, fmt.Errorf("apitool: %s: path parameters: %w", p, err)
			}
		}
		for _, m := range httpMethods {
			raw, ok := item[m]
			if !ok {
				continue
			}
			method := strings.ToUpper(m)
			if o.Include != nil && !o.Include(method, p) {
				continue
			}
			var od operationDoc
			if err := json.Unmarshal(raw, &od); err != nil {
				return nil, fmt.Errorf("apitool: %s %s: %w", method, p, err)
			}
			op, ok, err := r.build(method, p, &od, shared)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			op.readOnly = readOnlyOperation(o, method, p, od.AIReadOnly)
			op.name = uniqueName(o.Prefix+toolName(method, p, od.OperationID), seen)
			ops = append(ops, op)
		}
	}
	return ops, nil
}

// resolver resolves local component references.
type resolver struct {
	doc *document
}

// build assembles one operation. ok is false when the operation cannot be
// offered as a tool (a request body that is not JSON).
func (r *resolver) build(method, path string, od *operationDoc, shared []json.RawMessage) (operation, bool, error) {
	op := operation{method: method, path: path}

	// Operation parameters override path-level ones with the same name and
	// location.
	params := make([]parameterDoc, 0, len(shared)+len(od.Parameters))
	index := make(map[string]int)
	add := func(raw json.RawMessage) error {
		pd, err := r.parameter(raw)
		if err != nil {
			return fmt.Errorf("apitool: %s %s: %w", method, path, err)
		}
		key := pd.In + ":" + pd.Name
		if i, ok := index[key]; ok {
			params[i] = pd
			return nil
		}
		index[key] = len(params)
		params = append(params, pd)
		return nil
	}
	for _, raw := range shared {
		if err := add(raw); err != nil {
			return op, false, err
		}
	}
	for _, raw := range od.Parameters {
		if err := add(raw); err != nil {
			return op, false, err
		}
	}

	props := make(map[string]json.RawMessage)
	var required []string
	for _, pd := range params {
		switch pd.In {
		case "path", "query":
		default:
			// Header and cookie parameters are set by the executor from the
			// caller's identity, never by the model.
			continue
		}
		if pd.Name == "" {
			continue
		}
		schema, err := r.schema(pd.Schema, pd.Description)
		if err != nil {
			return op, false, fmt.Errorf("apitool: %s %s: parameter %s: %w", method, path, pd.Name, err)
		}
		props[pd.Name] = schema
		if pd.In == "path" {
			op.pathParams = append(op.pathParams, pd.Name)
			required = append(required, pd.Name)
		} else {
			op.queryParams = append(op.queryParams, pd.Name)
			if pd.Required {
				required = append(required, pd.Name)
			}
		}
	}
	// Path parameters the template names but the parameter list omits.
	for _, name := range templateParams(path) {
		if _, ok := props[name]; ok {
			continue
		}
		props[name] = json.RawMessage(`{"type":"string"}`)
		op.pathParams = append(op.pathParams, name)
		required = append(required, name)
	}

	if len(od.RequestBody) > 0 {
		rb, err := r.requestBody(od.RequestBody)
		if err != nil {
			return op, false, fmt.Errorf("apitool: %s %s: %w", method, path, err)
		}
		content, ok := rb.Content["application/json"]
		if !ok {
			for ct, c := range rb.Content {
				if strings.HasSuffix(strings.ToLower(ct), "+json") {
					content, ok = c, true
					break
				}
			}
		}
		if !ok {
			return op, false, nil
		}
		schema, err := r.schema(content.Schema, "The JSON request body.")
		if err != nil {
			return op, false, fmt.Errorf("apitool: %s %s: request body: %w", method, path, err)
		}
		props["body"] = schema
		op.hasBody = true
		if rb.Required {
			required = append(required, "body")
		}
	}

	full := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		full["required"] = required
	}
	schema, err := json.Marshal(full)
	if err != nil {
		return op, false, fmt.Errorf("apitool: %s %s: schema: %w", method, path, err)
	}
	op.schema = schema
	op.description = describe(method, path, od)
	return op, true, nil
}

// parameter resolves a parameter that may be a $ref.
func (r *resolver) parameter(raw json.RawMessage) (parameterDoc, error) {
	var pd parameterDoc
	if err := json.Unmarshal(raw, &pd); err != nil {
		return pd, fmt.Errorf("parameter: %w", err)
	}
	if pd.Ref == "" {
		return pd, nil
	}
	const prefix = "#/components/parameters/"
	if !strings.HasPrefix(pd.Ref, prefix) {
		return pd, fmt.Errorf("unsupported parameter reference %q", pd.Ref)
	}
	target, ok := r.doc.Components.Parameters[strings.TrimPrefix(pd.Ref, prefix)]
	if !ok {
		return pd, fmt.Errorf("unknown parameter reference %q", pd.Ref)
	}
	var resolved parameterDoc
	if err := json.Unmarshal(target, &resolved); err != nil {
		return pd, fmt.Errorf("parameter %s: %w", pd.Ref, err)
	}
	if resolved.Ref != "" {
		return pd, fmt.Errorf("parameter %s: nested reference not supported", pd.Ref)
	}
	return resolved, nil
}

// requestBody resolves a request body that may be a $ref.
func (r *resolver) requestBody(raw json.RawMessage) (requestBodyDoc, error) {
	var rb requestBodyDoc
	if err := json.Unmarshal(raw, &rb); err != nil {
		return rb, fmt.Errorf("request body: %w", err)
	}
	if rb.Ref == "" {
		return rb, nil
	}
	const prefix = "#/components/requestBodies/"
	if !strings.HasPrefix(rb.Ref, prefix) {
		return rb, fmt.Errorf("unsupported request body reference %q", rb.Ref)
	}
	target, ok := r.doc.Components.RequestBodies[strings.TrimPrefix(rb.Ref, prefix)]
	if !ok {
		return rb, fmt.Errorf("unknown request body reference %q", rb.Ref)
	}
	var resolved requestBodyDoc
	if err := json.Unmarshal(target, &resolved); err != nil {
		return rb, fmt.Errorf("request body %s: %w", rb.Ref, err)
	}
	return resolved, nil
}

// schema returns raw with every $ref resolved. description is added when
// the schema has none.
func (r *resolver) schema(raw json.RawMessage, description string) (json.RawMessage, error) {
	var v any = map[string]any{"type": "string"}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("schema: %w", err)
		}
	}
	resolved, err := r.resolve(v, map[string]bool{})
	if err != nil {
		return nil, err
	}
	if m, ok := resolved.(map[string]any); ok && description != "" {
		if _, has := m["description"]; !has {
			m["description"] = description
		}
	}
	out, err := json.Marshal(resolved)
	if err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	return out, nil
}

// resolve walks v and replaces every local schema reference with its
// target. visiting holds the references on the current path so a cycle is
// cut with a plain object instead of recursing forever.
func (r *resolver) resolve(v any, visiting map[string]bool) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		if ref, ok := x["$ref"].(string); ok {
			const prefix = "#/components/schemas/"
			if !strings.HasPrefix(ref, prefix) {
				return map[string]any{"description": "unsupported reference " + ref}, nil
			}
			name := strings.TrimPrefix(ref, prefix)
			if visiting[name] {
				return map[string]any{"type": "object", "description": "recursive reference to " + name}, nil
			}
			target, ok := r.doc.Components.Schemas[name]
			if !ok {
				return nil, fmt.Errorf("unknown schema reference %q", ref)
			}
			var tv any
			if err := json.Unmarshal(target, &tv); err != nil {
				return nil, fmt.Errorf("schema %s: %w", name, err)
			}
			visiting[name] = true
			resolved, err := r.resolve(tv, visiting)
			delete(visiting, name)
			if err != nil {
				return nil, err
			}
			// Sibling keywords next to $ref (description, for example) are
			// kept when the target has none.
			if rm, ok := resolved.(map[string]any); ok {
				for k, val := range x {
					if k == "$ref" {
						continue
					}
					if _, has := rm[k]; !has {
						rm[k] = val
					}
				}
			}
			return resolved, nil
		}
		out := make(map[string]any, len(x))
		for k, val := range x {
			rv, err := r.resolve(val, visiting)
			if err != nil {
				return nil, err
			}
			out[k] = rv
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			rv, err := r.resolve(val, visiting)
			if err != nil {
				return nil, err
			}
			out[i] = rv
		}
		return out, nil
	default:
		return v, nil
	}
}

// describe builds the tool description: summary, description, then the
// route and tag so the model can relate the tool to the API.
func describe(method, path string, od *operationDoc) string {
	var parts []string
	if s := strings.TrimSpace(od.Summary); s != "" {
		parts = append(parts, s)
	}
	if d := strings.TrimSpace(od.Description); d != "" && d != od.Summary {
		parts = append(parts, d)
	}
	route := method + " " + path
	if len(od.Tags) > 0 {
		route += " (" + strings.Join(od.Tags, ", ") + ")"
	}
	parts = append(parts, route)
	return strings.Join(parts, "\n")
}

var templateParamPattern = regexp.MustCompile(`\{([^}/]+)\}`)

// templateParams returns the parameter names a path template declares.
func templateParams(path string) []string {
	var names []string
	for _, m := range templateParamPattern.FindAllStringSubmatch(path, -1) {
		names = append(names, m[1])
	}
	return names
}

// toolName derives the name for one operation: the operationId in
// snake_case, or "{method}_{path}" when there is none.
func toolName(method, path, operationID string) string {
	if operationID != "" {
		return sanitize(snakeCase(operationID))
	}
	p := templateParamPattern.ReplaceAllString(path, "$1")
	return sanitize(strings.ToLower(method) + "_" + p)
}

// snakeCase converts camelCase and PascalCase to snake_case, keeping
// acronyms together: getAPITokens becomes get_api_tokens.
func snakeCase(s string) string {
	runes := []rune(s)
	var b strings.Builder
	for i, r := range runes {
		if unicode.IsUpper(r) {
			if i > 0 {
				prev := runes[i-1]
				nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
				if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
					b.WriteByte('_')
				}
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// sanitize keeps the name within ai.ToolNamePattern: anything that is not
// a letter, digit, underscore, or dash becomes an underscore, runs are
// collapsed, and the result is cut to 64 characters.
func sanitize(s string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-'
		switch {
		case ok:
			b.WriteRune(r)
			lastUnderscore = false
		case lastUnderscore:
		default:
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		out = "op"
	}
	if len(out) > 64 {
		out = strings.TrimRight(out[:64], "_")
	}
	return out
}

// uniqueName appends a counter when name was used before, keeping the
// result within the length limit.
func uniqueName(name string, seen map[string]int) string {
	name = sanitize(name)
	if seen[name] == 0 {
		seen[name] = 1
		return name
	}
	for {
		seen[name]++
		suffix := "_" + strconv.Itoa(seen[name])
		candidate := name
		if len(candidate)+len(suffix) > 64 {
			candidate = candidate[:64-len(suffix)]
		}
		candidate += suffix
		if seen[candidate] == 0 {
			seen[candidate] = 1
			return candidate
		}
	}
}

// readOnlyOperation decides whether an operation is read-only: the host's
// Options.ReadOnly first, then the x-ai-read-only extension, then the
// method.
func readOnlyOperation(o Options, method, path string, ext *bool) bool {
	if o.ReadOnly != nil {
		if ro, ok := o.ReadOnly(method, path); ok {
			return ro
		}
	}
	if ext != nil {
		return *ext
	}
	return readOnlyMethod(method)
}

// readOnlyMethod reports whether an HTTP method observes without changing.
func readOnlyMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

// checkName validates a generated name against the contract.
func checkName(name string) error {
	if !ai.ToolNamePattern.MatchString(name) {
		return fmt.Errorf("%w: %q", ai.ErrInvalidToolName, name)
	}
	return nil
}
