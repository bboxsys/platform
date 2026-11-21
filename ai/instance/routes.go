package instance

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/gorilla/mux"
)

// DefaultExcludedSegments are the path segments RouteTools leaves out of
// the tool surface when Exclude is nil: streams never end, and uploads and
// raw protocol proxies are not something a model should drive. A product
// extends the list with its own (credential exports, arbitrary proxies)
// through ExcludePathSegments.
var DefaultExcludedSegments = []string{"events", "stream", "realtime", "upload", "grpc"}

// ExcludePathSegments returns an Exclude function that rejects every path
// containing one of the segments.
func ExcludePathSegments(segments ...string) func(path string) bool {
	set := make(map[string]bool, len(segments))
	for _, s := range segments {
		set[s] = true
	}
	return func(path string) bool {
		for _, segment := range strings.Split(path, "/") {
			if set[segment] {
				return true
			}
		}
		return false
	}
}

// RouteOperation names operations admitted explicitly: routes the router
// serves behind a wildcard template (a proxy), which the walk skips because
// a wildcard is never offered to the model as a whole.
type RouteOperation struct {
	Path    string
	Methods []string
}

// RouteToolsOptions configures RouteTools.
type RouteToolsOptions struct {
	// Router is the service's router after every route is registered.
	// Required.
	Router *mux.Router
	// Root is the API root the tools are derived from, for example
	// "/api/v1/inventory"; only routes under it are considered. Required.
	Root string
	// Title is the document's info.title; default Root.
	Title string
	// Exclude rejects a path template from the tool surface. Nil means
	// ExcludePathSegments(DefaultExcludedSegments...). It applies to the
	// generated tools and the generic request tool alike.
	Exclude func(path string) bool
	// Queries names the query parameters an operation accepts when no
	// contract describes it (the walk cannot see them). Optional; a
	// contract's declared parameters always win.
	Queries func(path string) []string
	// SideEffectGET marks GET operations that change state (a refresh, a
	// parse, a redirect that creates something), which are then published
	// with x-ai-read-only: false so they need write approval. Optional.
	SideEffectGET func(path string) bool
	// Admit adds operations the walk does not produce. Optional.
	Admit []RouteOperation
	// Contracts are OpenAPI documents whose operations replace the
	// inferred ones: declared parameters, schemas and descriptions. A
	// contract naming an operation that is neither registered under Root
	// nor admitted is an error, so a document cannot widen the surface.
	Contracts [][]byte
}

// underRoot reports whether path is root itself or a route beneath it.
func underRoot(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}

// RouteTools derives an OpenAPI document and an Include filter from the
// routes a router has registered under Root, so a service exposes its API
// to the assistant without maintaining a second description of it. Pass
// the document as Options.OpenAPI and the filter as Options.ToolFilter;
// the filter is what keeps the generic request tool on the same surface
// as the generated tools. Streaming routes, uploads and wildcard templates
// (gorilla patterns such as {path:.*}) are left out; the service admits
// the specific operations behind a wildcard with Admit.
func RouteTools(o RouteToolsOptions) (spec []byte, include func(method, path string) bool, err error) {
	if o.Router == nil {
		return nil, nil, errors.New("ai/instance: RouteTools needs a Router")
	}
	root := strings.TrimRight(o.Root, "/")
	if root == "" {
		return nil, nil, errors.New("ai/instance: RouteTools needs a Root")
	}
	if o.Title == "" {
		o.Title = root
	}
	exclude := o.Exclude
	if exclude == nil {
		exclude = ExcludePathSegments(DefaultExcludedSegments...)
	}
	type routeMethod struct{ method, path string }
	var allowed []routeMethod
	paths := map[string]map[string]any{}
	add := func(path string, methods []string) {
		if paths[path] == nil {
			paths[path] = map[string]any{}
		}
		for _, method := range methods {
			method = strings.ToUpper(method)
			if method == http.MethodOptions || method == http.MethodHead {
				continue
			}
			allowed = append(allowed, routeMethod{method, path})
			parameters := []any{}
			if o.Queries != nil {
				for _, name := range o.Queries(path) {
					parameters = append(parameters, map[string]any{"name": name, "in": "query", "schema": map[string]string{"type": "string"}})
				}
			}
			op := map[string]any{
				"summary":    method + " " + path,
				"parameters": parameters,
				"responses":  map[string]any{"200": map[string]string{"description": "Service response"}},
			}
			if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch {
				op["requestBody"] = map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "additionalProperties": true}}}}
			}
			if method == http.MethodGet && o.SideEffectGET != nil && o.SideEffectGET(path) {
				op["x-ai-read-only"] = false
			}
			paths[path][strings.ToLower(method)] = op
		}
	}
	err = o.Router.Walk(func(route *mux.Route, _ *mux.Router, _ []*mux.Route) error {
		path, err := route.GetPathTemplate()
		if err != nil || !underRoot(path, root) || exclude(path) {
			return nil
		}
		methods, err := route.GetMethods()
		// A template with a pattern ({path:.*}) is a wildcard, never a tool.
		if err != nil || strings.Contains(path, ":") {
			return nil
		}
		add(path, methods)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	for _, op := range o.Admit {
		if !underRoot(op.Path, root) {
			return nil, nil, fmt.Errorf("ai/instance: admitted operation %s is outside %s", op.Path, root)
		}
		add(op.Path, op.Methods)
	}
	if len(allowed) == 0 {
		return nil, nil, fmt.Errorf("ai/instance: no API routes registered under %s", root)
	}
	for _, contract := range o.Contracts {
		if len(contract) == 0 {
			continue
		}
		var document struct {
			Paths map[string]map[string]json.RawMessage `json:"paths"`
		}
		if err := json.Unmarshal(contract, &document); err != nil {
			return nil, nil, fmt.Errorf("ai/instance: API contract: %w", err)
		}
		for path, operations := range document.Paths {
			for method, raw := range operations {
				if _, exists := paths[path][strings.ToLower(method)]; !exists {
					return nil, nil, fmt.Errorf("ai/instance: API contract names unregistered operation %s %s", strings.ToUpper(method), path)
				}
				var operation map[string]any
				if err := json.Unmarshal(raw, &operation); err != nil {
					return nil, nil, fmt.Errorf("ai/instance: API contract %s %s: %w", strings.ToUpper(method), path, err)
				}
				paths[path][strings.ToLower(method)] = operation
			}
		}
	}
	sort.Slice(allowed, func(i, j int) bool {
		if allowed[i].path != allowed[j].path {
			return allowed[i].path < allowed[j].path
		}
		return allowed[i].method < allowed[j].method
	})
	spec, err = json.Marshal(map[string]any{"openapi": "3.0.3", "info": map[string]string{"title": o.Title, "version": "1.0.0"}, "paths": paths})
	if err != nil {
		return nil, nil, err
	}
	include = func(method, path string) bool {
		for _, route := range allowed {
			if method == route.method && PathMatches(path, route.path) {
				return true
			}
		}
		return false
	}
	return spec, include, nil
}
