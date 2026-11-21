package instance

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/bboxsys/platform/ai/apitool"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

func noop(http.ResponseWriter, *http.Request) {}

func TestRouteToolsFollowsRegisteredRoutes(t *testing.T) {
	router := mux.NewRouter()
	for _, path := range []string{"/api/v1/things", "/api/v1/things/{id}/secret", "/api/v1/things/events", "/api/v1/others"} {
		router.HandleFunc(path, noop).Methods("GET")
	}
	router.HandleFunc("/api/v1/things/{id}/refresh", noop).Methods("GET")
	router.HandleFunc("/api/v1/things/proxy/{path:.*}", noop)
	spec, include, err := RouteTools(RouteToolsOptions{
		Router:        router,
		Root:          "/api/v1/things",
		Exclude:       ExcludePathSegments(append(DefaultExcludedSegments, "secret")...),
		Queries:       func(string) []string { return []string{"limit"} },
		SideEffectGET: func(path string) bool { return strings.HasSuffix(path, "/refresh") },
		Admit:         []RouteOperation{{Path: "/api/v1/things/proxy/items", Methods: []string{"GET", "POST"}}},
	})
	require.NoError(t, err)
	require.True(t, json.Valid(spec))
	require.True(t, include("GET", "/api/v1/things"))
	require.True(t, include("GET", "/api/v1/things/proxy/items"), "admitted operation")
	require.True(t, include("POST", "/api/v1/things/proxy/items"))
	for _, path := range []string{"/api/v1/things/one/secret", "/api/v1/things/events", "/api/v1/others", "/api/v1/things/proxy/anything", "/api/v1/ai/status"} {
		require.False(t, include("GET", path), path)
	}
	require.False(t, include("DELETE", "/api/v1/things"), "method not registered")

	ops, err := apitool.Operations(spec, apitool.Options{Prefix: "things_"})
	require.NoError(t, err)
	byName := map[string]apitool.Operation{}
	for _, op := range ops {
		byName[op.Name] = op
	}
	require.Contains(t, byName, "things_get_api_v1_things")
	require.True(t, byName["things_get_api_v1_things"].ReadOnly)
	require.False(t, byName["things_get_api_v1_things_id_refresh"].ReadOnly, "a GET with side effects needs approval")

	var doc struct {
		Paths map[string]map[string]struct {
			Parameters []struct{ Name string } `json:"parameters"`
		} `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(spec, &doc))
	require.Equal(t, "limit", doc.Paths["/api/v1/things"]["get"].Parameters[0].Name)
}

func TestRouteToolsContractOverlay(t *testing.T) {
	contract := []byte(`{"openapi":"3.0.3","info":{"title":"t","version":"1"},"paths":{"/api/v1/things/summary":{"get":{"operationId":"summarizeThings","summary":"Count things","description":"Use before full lists.","parameters":[{"name":"kind","in":"query","required":true,"schema":{"type":"string"}}],"responses":{"200":{"description":"ok"}}}}}}`)
	router := mux.NewRouter()
	router.HandleFunc("/api/v1/things/summary", noop).Methods("GET")
	router.HandleFunc("/api/v1/things", noop).Methods("GET")
	spec, _, err := RouteTools(RouteToolsOptions{Router: router, Root: "/api/v1/things", Contracts: [][]byte{contract}})
	require.NoError(t, err)
	tools, err := apitool.FromOpenAPI(spec, router, apitool.Options{Prefix: "things_"})
	require.NoError(t, err)
	var summary string
	for _, tool := range tools {
		if tool.Name() == "things_summarize_things" {
			summary = tool.Description()
			var schema struct {
				Required []string `json:"required"`
			}
			require.NoError(t, json.Unmarshal(tool.InputSchema(), &schema))
			require.Equal(t, []string{"kind"}, schema.Required, "declared parameters replace inferred ones")
		}
	}
	require.Contains(t, summary, "Use before full lists.")

	other := mux.NewRouter()
	other.HandleFunc("/api/v1/things/other", noop).Methods("GET")
	_, _, err = RouteTools(RouteToolsOptions{Router: other, Root: "/api/v1/things", Contracts: [][]byte{contract}})
	require.Error(t, err, "a contract cannot widen the surface")

	empty := mux.NewRouter()
	_, _, err = RouteTools(RouteToolsOptions{Router: empty, Root: "/api/v1/things"})
	require.Error(t, err)
}
