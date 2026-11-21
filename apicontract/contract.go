// Package apicontract describes a query operation once, for both the
// OpenAPI document a client or an assistant reads and the validation the
// handler applies: the required inputs, the supported filters, paging and
// the response schema. A request naming a filter the operation does not
// list is refused with a problem document before the handler runs, so the
// surface the model is told about is the surface it gets.
//
//	var api = apicontract.New("Inventory", "/api/v1/inventory",
//	    apicontract.Operation{Path: "/nodes/summary", ID: "summarizeNodes", Summary: "Count nodes per cluster",
//	        Parameters: append([]apicontract.Parameter{apicontract.Query("cluster_id", "One cluster", false)}, apicontract.Paging()...),
//	        ResponseSchema: apicontract.Schema[NodeSummary]()})
//	router.HandleFunc("/api/v1/inventory/openapi.json", auth.Bearer(api.ServeHTTP)).Methods("GET")
//	router.HandleFunc("/api/v1/inventory/nodes/summary", auth.Bearer(api.Wrap("GET", "/nodes/summary", summarizeNodes))).Methods("GET")
//
// The document is also what instance.RouteTools overlays on the routes it
// walks (RouteToolsOptions.Contracts), so the generated tools carry the
// declared parameters instead of inferred ones.
package apicontract

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

const (
	// DefaultPageLimit is the page size when a request names none.
	DefaultPageLimit = 100
	// MaxPageLimit is the largest page a request may ask for.
	MaxPageLimit = 200
)

// Parameter is one query parameter of an operation.
type Parameter struct {
	Name        string         `json:"name"`
	In          string         `json:"in"`
	Description string         `json:"description"`
	Required    bool           `json:"required,omitempty"`
	Schema      map[string]any `json:"schema"`
}

// Operation is one documented operation. Method defaults to GET; ID and
// Summary are required. Responses is generated from ResponseSchema when
// empty.
type Operation struct {
	Method         string         `json:"-"`
	Path           string         `json:"-"`
	ID             string         `json:"operationId"`
	Summary        string         `json:"summary"`
	Description    string         `json:"description"`
	Parameters     []Parameter    `json:"parameters"`
	Responses      map[string]any `json:"responses"`
	ResponseSchema map[string]any `json:"-"`
}

// Contract is a set of operations and their OpenAPI document.
type Contract struct {
	document   []byte
	operations map[string]Operation
}

// Query is a required or optional string parameter.
func Query(name, description string, required bool) Parameter {
	return Parameter{Name: name, In: "query", Description: description, Required: required, Schema: map[string]any{"type": "string", "minLength": 1}}
}

// Boolean is an optional true/false parameter.
func Boolean(name, description string) Parameter {
	p := Query(name, description, false)
	p.Schema = map[string]any{"type": "boolean"}
	return p
}

// Choice is an optional parameter restricted to the given values.
func Choice(name, description string, values ...string) Parameter {
	p := Query(name, description, false)
	p.Schema["enum"] = values
	return p
}

// Paging returns the limit and offset parameters Pagination reads.
func Paging() []Parameter {
	return []Parameter{
		{Name: "limit", In: "query", Description: fmt.Sprintf("Maximum rows in this response; default %d, maximum %d. Totals describe the entire filtered result.", DefaultPageLimit, MaxPageLimit), Schema: map[string]any{"type": "integer", "minimum": 1, "maximum": MaxPageLimit, "default": DefaultPageLimit}},
		{Name: "offset", In: "query", Description: "Zero-based row offset. Use next_offset while has_more is true. Live data may change between requests.", Schema: map[string]any{"type": "integer", "minimum": 0, "default": 0}},
	}
}

// New builds the contract and its document. base is the path every
// operation's Path is relative to. It panics on an operation without an ID
// or summary, as a missing contract would otherwise surface only to the
// model.
func New(title, base string, operations ...Operation) *Contract {
	paths := map[string]map[string]Operation{}
	c := &Contract{operations: map[string]Operation{}}
	for _, operation := range operations {
		operation.Method = strings.ToUpper(operation.Method)
		if operation.Method == "" {
			operation.Method = http.MethodGet
		}
		if operation.ID == "" || operation.Summary == "" {
			panic("apicontract: an operation requires an ID and a summary")
		}
		if operation.Parameters == nil {
			operation.Parameters = []Parameter{}
		}
		if operation.Responses == nil {
			schema := operation.ResponseSchema
			if schema == nil {
				schema = map[string]any{}
			}
			operation.Responses = map[string]any{
				"200": map[string]any{"description": "Query result; scope, completeness and paging are described by the operation and response schema.", "content": map[string]any{"application/json": map[string]any{"schema": schema}}},
				"400": map[string]any{"description": "Invalid or unsupported parameters; see invalidParams for corrections."},
				"401": map[string]any{"description": "Caller authentication is required."},
			}
		}
		if paths[base+operation.Path] == nil {
			paths[base+operation.Path] = map[string]Operation{}
		}
		paths[base+operation.Path][strings.ToLower(operation.Method)] = operation
		c.operations[operation.Method+" "+operation.Path] = operation
	}
	var err error
	c.document, err = json.Marshal(map[string]any{
		"openapi":    "3.0.3",
		"info":       map[string]string{"title": title, "version": "1.0.0", "description": "Documented query operations. Calls keep the signed-in user's account scope and authorization."},
		"paths":      paths,
		"components": map[string]any{"securitySchemes": map[string]any{"bearerAuth": map[string]string{"type": "http", "scheme": "bearer"}}},
		"security":   []any{map[string]any{"bearerAuth": []string{}}},
	})
	if err != nil {
		panic(err)
	}
	return c
}

// Document is the OpenAPI 3 document.
func (c *Contract) Document() []byte { return c.document }

// ServeHTTP serves the document.
func (c *Contract) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(c.document)
}

// Wrap validates a request against the operation registered for method and
// path (relative to base) and answers a problem document for unknown
// filters, missing required inputs and values outside a parameter's
// schema; valid requests reach next. It panics when the operation is not
// in the contract. The accountID and userID query parameters are always
// accepted, because the platform's ID middleware reads them.
func (c *Contract) Wrap(method, path string, next http.HandlerFunc) http.HandlerFunc {
	op, ok := c.operations[strings.ToUpper(method)+" "+path]
	if !ok {
		panic("apicontract: missing operation: " + method + " " + path)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		allowed := map[string]bool{"accountID": true, "userID": true}
		invalid := []map[string]string{}
		query := r.URL.Query()
		for _, p := range op.Parameters {
			if p.In != "query" {
				continue
			}
			allowed[p.Name] = true
			values, found := query[p.Name]
			if !found {
				if p.Required {
					invalid = append(invalid, map[string]string{"name": p.Name, "reason": p.Description + " (required)"})
				}
				continue
			}
			for _, value := range values {
				if reason := invalidValue(p, value); reason != "" {
					invalid = append(invalid, map[string]string{"name": p.Name, "reason": reason})
				}
			}
		}
		for name := range query {
			if !allowed[name] {
				invalid = append(invalid, map[string]string{"name": name, "reason": "unsupported parameter for this operation; consult its OpenAPI contract"})
			}
		}
		if len(invalid) > 0 {
			sort.Slice(invalid, func(i, j int) bool { return invalid[i]["name"] < invalid[j]["name"] })
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": "Invalid query parameters", "status": http.StatusBadRequest, "detail": "Correct the listed parameters before retrying. Unsupported filters are not applied.", "invalidParams": invalid})
			return
		}
		next(w, r)
	}
}

// invalidValue reports why value does not fit the parameter's schema, or "".
func invalidValue(p Parameter, value string) string {
	reason := ""
	switch p.Schema["type"] {
	case "boolean":
		if value != "true" && value != "false" {
			reason = "must be true or false"
		}
	case "integer":
		n, err := strconv.Atoi(value)
		if err != nil {
			reason = "must be an integer"
		} else {
			if min, ok := p.Schema["minimum"].(int); ok && n < min {
				reason = fmt.Sprintf("must be at least %d", min)
			}
			if max, ok := p.Schema["maximum"].(int); ok && n > max {
				reason = fmt.Sprintf("must be at most %d", max)
			}
		}
	case "string":
		if strings.TrimSpace(value) == "" {
			reason = "must not be empty"
		}
	}
	if choices, ok := p.Schema["enum"].([]string); ok {
		valid := false
		for _, choice := range choices {
			valid = valid || value == choice
		}
		if !valid {
			reason = "must be one of: " + strings.Join(choices, ", ")
		}
	}
	return reason
}

// Page describes one page of a result: totals precede paging, so a reader
// knows the size of the whole filtered result before walking it.
type Page struct {
	Total      int  `json:"total"`
	Limit      int  `json:"limit"`
	Offset     int  `json:"offset"`
	HasMore    bool `json:"has_more"`
	NextOffset *int `json:"next_offset,omitempty"`
}

// Pagination reads limit and offset from the request (see Paging) and
// places them against total.
func Pagination(r *http.Request, total int) Page {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = DefaultPageLimit
	}
	if limit > MaxPageLimit {
		limit = MaxPageLimit
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	p := Page{Total: total, Limit: limit, Offset: offset, HasMore: offset < total && limit < total-offset}
	if p.HasMore {
		next := offset + limit
		p.NextOffset = &next
	}
	return p
}

// Bounds returns the half-open slice bounds of the page within total.
func (p Page) Bounds() (int, int) {
	start := min(p.Offset, p.Total)
	return start, min(start+p.Limit, p.Total)
}
