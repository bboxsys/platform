package apitool

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bboxsys/platform/ai"
	"github.com/gorilla/mux"
)

const testSpec = `{
  "openapi": "3.0.3",
  "info": {"title": "things", "version": "1"},
  "components": {
    "parameters": {
      "AccountID": {"name": "X-Blackbox-AccountID", "in": "header", "required": true, "schema": {"type": "string"}}
    },
    "schemas": {
      "Thing": {
        "type": "object",
        "required": ["name"],
        "properties": {
          "name": {"type": "string"},
          "owner": {"$ref": "#/components/schemas/Owner"},
          "parent": {"$ref": "#/components/schemas/Thing"}
        }
      },
      "Owner": {"type": "object", "properties": {"email": {"type": "string", "format": "email"}}}
    }
  },
  "paths": {
    "/api/v1/things": {
      "parameters": [{"$ref": "#/components/parameters/AccountID"}],
      "get": {
        "operationId": "listThings",
        "summary": "List things",
        "tags": ["Things"],
        "parameters": [
          {"name": "limit", "in": "query", "schema": {"type": "integer"}, "description": "Page size"},
          {"name": "tag", "in": "query", "schema": {"type": "array", "items": {"type": "string"}}}
        ]
      },
      "post": {
        "operationId": "createThing",
        "summary": "Create a thing",
        "description": "Adds a thing to the account.",
        "tags": ["Things"],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Thing"}}}}
      }
    },
    "/api/v1/things/{id}": {
      "get": {
        "operationId": "getAPIThing",
        "summary": "Get a thing",
        "parameters": [
          {"$ref": "#/components/parameters/AccountID"},
          {"name": "id", "in": "path", "required": true, "schema": {"type": "string"}}
        ]
      },
      "delete": {
        "summary": "Delete a thing",
        "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "string"}}]
      }
    },
    "/api/v1/things/{id}/photo": {
      "post": {
        "operationId": "uploadPhoto",
        "requestBody": {"content": {"multipart/form-data": {"schema": {"type": "object"}}}},
        "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "string"}}]
      }
    }
  }
}`

// testRouter is three routes behind a middleware that requires the
// caller's bearer and account headers, like the admin's ID and Bearer
// wrappers.
func testRouter(t *testing.T, calls *int32) http.Handler {
	t.Helper()
	r := mux.NewRouter()
	r.HandleFunc("/api/v1/things", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"limit": req.URL.Query().Get("limit"),
			"tags":  req.URL.Query()["tag"],
			"items": []string{"a", "b"},
		})
	}).Methods(http.MethodGet)
	r.HandleFunc("/api/v1/things/{id}", func(w http.ResponseWriter, req *http.Request) {
		id := mux.Vars(req)["id"]
		if id != "one" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"not found"}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"one","name":"first"}`)
	}).Methods(http.MethodGet)
	r.HandleFunc("/api/v1/things", func(w http.ResponseWriter, req *http.Request) {
		if ct := req.Header.Get("Content-Type"); ct != "application/json" {
			http.Error(w, "content type "+ct, http.StatusUnsupportedMediaType)
			return
		}
		var in map[string]any
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"created": in["name"]})
	}).Methods(http.MethodPost)

	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		atomic.AddInt32(calls, 1)
		if req.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if req.Header.Get(HeaderAccountID) != "acct" {
			http.Error(w, "no account", http.StatusForbidden)
			return
		}
		if req.Header.Get(HeaderUserID) != "user" {
			http.Error(w, "no user", http.StatusForbidden)
			return
		}
		if req.Header.Get(HeaderTransport) != TransportAI {
			http.Error(w, "no transport", http.StatusForbidden)
			return
		}
		if req.Header.Get("Accept") != "application/json" {
			http.Error(w, "no accept", http.StatusNotAcceptable)
			return
		}
		r.ServeHTTP(w, req)
	})
}

func callerCtx() context.Context {
	return ai.WithCaller(context.Background(), ai.Caller{AccountID: "acct", UserID: "user", Bearer: "tok"})
}

func byName(t *testing.T, tools []ai.Tool, name string) ai.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("tool %q not generated; have %v", name, names(tools))
	return nil
}

func names(tools []ai.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name())
	}
	return out
}

func TestFromOpenAPINamesAndSchemas(t *testing.T) {
	var calls int32
	tools, err := FromOpenAPI([]byte(testSpec), testRouter(t, &calls), Options{Prefix: "t_"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"t_list_things", "t_create_thing", "t_get_api_thing", "t_delete_api_v1_things_id"}
	if got := names(tools); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("names = %v, want %v (multipart upload must be skipped)", got, want)
	}
	for _, name := range names(tools) {
		if !ai.ToolNamePattern.MatchString(name) {
			t.Errorf("name %q does not match the pattern", name)
		}
	}

	list := byName(t, tools, "t_list_things")
	if !list.ReadOnly() {
		t.Error("GET should be read-only")
	}
	if !strings.Contains(list.Description(), "List things") || !strings.Contains(list.Description(), "GET /api/v1/things (Things)") {
		t.Errorf("description = %q", list.Description())
	}
	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(list.InputSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Type != "object" {
		t.Errorf("type = %q", schema.Type)
	}
	if _, ok := schema.Properties["limit"]; !ok {
		t.Error("query parameter limit missing")
	}
	if _, ok := schema.Properties["X-Blackbox-AccountID"]; ok {
		t.Error("header parameter must not be in the schema")
	}
	if len(schema.Required) != 0 {
		t.Errorf("required = %v, want none", schema.Required)
	}

	get := byName(t, tools, "t_get_api_thing")
	if err := json.Unmarshal(get.InputSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "id" {
		t.Errorf("required = %v, want [id]", schema.Required)
	}

	create := byName(t, tools, "t_create_thing")
	if create.ReadOnly() {
		t.Error("POST must not be read-only")
	}
	if !strings.Contains(create.Description(), "Adds a thing") {
		t.Errorf("description = %q", create.Description())
	}
	if err := json.Unmarshal(create.InputSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "body" {
		t.Errorf("required = %v, want [body]", schema.Required)
	}
	body := string(schema.Properties["body"])
	if strings.Contains(body, "$ref") {
		t.Errorf("body schema still has a $ref: %s", body)
	}
	if !strings.Contains(body, `"format":"email"`) {
		t.Errorf("nested Owner ref not resolved: %s", body)
	}
	if !strings.Contains(body, "recursive reference to Thing") {
		t.Errorf("cycle not guarded: %s", body)
	}
	if !strings.Contains(body, `"description":"The JSON request body."`) {
		t.Errorf("body description missing: %s", body)
	}
}

func TestOperations(t *testing.T) {
	ops, err := Operations([]byte(testSpec), Options{Prefix: "t_", Include: func(method, path string) bool {
		return method == http.MethodGet
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 {
		t.Fatalf("ops = %+v, want two GETs", ops)
	}
	if ops[0].Name != "t_list_things" || ops[0].Method != "GET" || ops[0].Path != "/api/v1/things" || !ops[0].ReadOnly {
		t.Errorf("ops[0] = %+v", ops[0])
	}
	if ops[1].Name != "t_get_api_thing" || ops[1].Path != "/api/v1/things/{id}" {
		t.Errorf("ops[1] = %+v", ops[1])
	}
}

func TestExecute(t *testing.T) {
	var calls int32
	tools, err := FromOpenAPI([]byte(testSpec), testRouter(t, &calls), Options{Prefix: "t_"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := callerCtx()

	res, err := byName(t, tools, "t_list_things").Execute(ctx, json.RawMessage(`{"limit": 5, "tag": ["x", "y"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("list failed: %s", res.Content)
	}
	if !strings.Contains(res.Content, `"limit": "5"`) || !strings.Contains(res.Content, `"y"`) {
		t.Errorf("query not forwarded or body not pretty printed: %s", res.Content)
	}
	if len(res.JSON) == 0 || !json.Valid(res.JSON) {
		t.Errorf("JSON not set: %s", res.JSON)
	}

	res, err = byName(t, tools, "t_get_api_thing").Execute(ctx, json.RawMessage(`{"id":"one"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, `"name": "first"`) {
		t.Errorf("get = %+v", res)
	}

	res, err = byName(t, tools, "t_get_api_thing").Execute(ctx, json.RawMessage(`{"id":"missing"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.HasPrefix(res.Content, "HTTP 404 Not Found\n") || !strings.Contains(res.Content, "not found") {
		t.Errorf("404 = %+v", res)
	}

	res, err = byName(t, tools, "t_get_api_thing").Execute(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "missing path parameter") {
		t.Errorf("missing id = %+v", res)
	}

	res, err = byName(t, tools, "t_create_thing").Execute(ctx, json.RawMessage(`{"body": {"name": "new"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, `"created": "new"`) {
		t.Errorf("create = %+v", res)
	}
}

func TestExecuteWithoutCaller(t *testing.T) {
	var calls int32
	tools, err := FromOpenAPI([]byte(testSpec), testRouter(t, &calls), Options{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := byName(t, tools, "list_things").Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "no caller") {
		t.Errorf("result = %+v", res)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Error("handler must not be called without a caller")
	}
}

func TestExecuteWrongBearer(t *testing.T) {
	var calls int32
	tools, err := FromOpenAPI([]byte(testSpec), testRouter(t, &calls), Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ai.WithCaller(context.Background(), ai.Caller{AccountID: "acct", UserID: "user", Bearer: "other"})
	res, err := byName(t, tools, "list_things").Execute(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.HasPrefix(res.Content, "HTTP 401") {
		t.Errorf("result = %+v", res)
	}
}

func TestTruncation(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 100)))
	})
	tool := NewRequestTool(h, Options{MaxBodyBytes: 10})
	res, err := tool.Execute(callerCtx(), json.RawMessage(`{"method":"get","path":"/api/anything"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Content, "xxxxxxxxxx\n[truncated at 10 bytes]") {
		t.Errorf("content = %q", res.Content)
	}
}

func TestRequestTool(t *testing.T) {
	var calls int32
	tool := NewRequestTool(testRouter(t, &calls), Options{Prefix: "t_"})
	if tool.Name() != "t_api_request" {
		t.Errorf("name = %q", tool.Name())
	}
	if tool.ReadOnly() {
		t.Error("api_request must not be read-only")
	}
	ctx := callerCtx()

	res, err := tool.Execute(ctx, json.RawMessage(`{"method":"get","path":"/api/v1/things?limit=1","query":{"tag":"z"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, `"limit": "1"`) || !strings.Contains(res.Content, `"z"`) {
		t.Errorf("get = %+v", res)
	}

	res, err = tool.Execute(ctx, json.RawMessage(`{"method":"POST","path":"/api/v1/things","body":{"name":"raw"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, `"created": "raw"`) {
		t.Errorf("post = %+v", res)
	}

	res, err = tool.Execute(ctx, json.RawMessage(`{"method":"GET","path":"api/v1/things"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Errorf("relative path accepted: %+v", res)
	}

	res, err = tool.Execute(context.Background(), json.RawMessage(`{"method":"GET","path":"/api/v1/things"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "no caller") {
		t.Errorf("no caller = %+v", res)
	}
}

func TestNaming(t *testing.T) {
	cases := map[string]string{
		"listThings":    "list_things",
		"getAPITokens":  "get_api_tokens",
		"CreateAccount": "create_account",
		"getV2Thing":    "get_v2_thing",
		"already_snake": "already_snake",
		"with.dots/ok":  "with_dots_ok",
	}
	for in, want := range cases {
		if got := toolName("GET", "/x", in); got != want {
			t.Errorf("toolName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := toolName("DELETE", "/api/v1/things/{id}/tags/{tag}", ""); got != "delete_api_v1_things_id_tags_tag" {
		t.Errorf("fallback = %q", got)
	}
	long := strings.Repeat("a", 80)
	if got := sanitize(long); len(got) != 64 {
		t.Errorf("sanitize length = %d", len(got))
	}
	seen := map[string]int{}
	if a, b := uniqueName("dup", seen), uniqueName("dup", seen); a != "dup" || b != "dup_2" {
		t.Errorf("unique = %q, %q", a, b)
	}
}

func TestDuplicateOperationIDs(t *testing.T) {
	spec := `{"openapi":"3.0.3","paths":{
	  "/a": {"get": {"operationId": "same"}},
	  "/b": {"get": {"operationId": "same"}}
	}}`
	ops, err := Operations([]byte(spec), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 || ops[0].Name != "same" || ops[1].Name != "same_2" {
		t.Errorf("ops = %+v", ops)
	}
}

func TestBadSpec(t *testing.T) {
	if _, err := FromOpenAPI([]byte(`not json`), http.NotFoundHandler(), Options{}); err == nil {
		t.Error("expected a parse error")
	}
	if _, err := FromOpenAPI([]byte(`{"openapi":"3.0.3","paths":{}}`), http.NotFoundHandler(), Options{}); err == nil {
		t.Error("expected an error for no paths")
	}
	spec := `{"openapi":"3.0.3","paths":{"/a":{"post":{"requestBody":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/Nope"}}}}}}}}`
	if _, err := FromOpenAPI([]byte(spec), http.NotFoundHandler(), Options{}); err == nil || !strings.Contains(err.Error(), "unknown schema reference") {
		t.Errorf("err = %v", err)
	}
}

func TestRequestToolHonoursIncludeAndPrefix(t *testing.T) {
	var calls int32
	tool := NewRequestTool(testRouter(t, &calls), Options{Prefix: "t_", Include: func(method, path string) bool {
		return !strings.HasPrefix(path, "/api/v1/things/")
	}})
	if tool.ReadOnly() {
		t.Error("api_request must not be read-only even with a filter")
	}
	ctx := callerCtx()
	refused := []string{
		`{"method":"GET","path":"/api/v1/things/one"}`,
		`{"method":"GET","path":"/api/v1/things/%6Fne"}`,
		`{"method":"GET","path":"/api/v1/x/../things/one"}`,
		`{"method":"GET","path":"/api/v1//things/one"}`,
		`{"method":"GET","path":"/other"}`,
		`{"method":"GET","path":"/apiv1/things"}`,
	}
	for _, in := range refused {
		res, err := tool.Execute(ctx, json.RawMessage(in))
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Errorf("%s was not refused: %+v", in, res)
		}
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Errorf("refused calls reached the handler %d times", n)
	}
	res, err := tool.Execute(ctx, json.RawMessage(`{"method":"GET","path":"/api/v1/things"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Errorf("included path refused: %+v", res)
	}
}

func TestReadOnlyOverrides(t *testing.T) {
	spec := strings.Replace(testSpec, `"operationId": "listThings",`, `"operationId": "listThings", "x-ai-read-only": false,`, 1)
	ops, err := Operations([]byte(spec), Options{})
	if err != nil {
		t.Fatal(err)
	}
	ro := map[string]bool{}
	for _, op := range ops {
		ro[op.Name] = op.ReadOnly
	}
	if ro["list_things"] {
		t.Error("x-ai-read-only: false must make a GET a write")
	}
	if !ro["get_api_thing"] {
		t.Error("a plain GET stays read-only")
	}

	tools, err := FromOpenAPI([]byte(spec), testRouter(t, new(int32)), Options{ReadOnly: func(method, path string) (bool, bool) {
		switch {
		case method == http.MethodGet && path == "/api/v1/things/{id}":
			return false, true
		case method == http.MethodGet && path == "/api/v1/things":
			return true, true
		}
		return false, false
	}})
	if err != nil {
		t.Fatal(err)
	}
	if byName(t, tools, "get_api_thing").ReadOnly() {
		t.Error("host override to write ignored")
	}
	if !byName(t, tools, "list_things").ReadOnly() {
		t.Error("host override takes precedence over the extension")
	}
	if byName(t, tools, "create_thing").ReadOnly() {
		t.Error("a POST without an override stays a write")
	}
}
