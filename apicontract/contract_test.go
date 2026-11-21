package apicontract

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestContractRejectsUnknownAndMissingInputsBeforeDispatch(t *testing.T) {
	c := New("test", "/api", Operation{Path: "/resources", ID: "list", Summary: "List", Parameters: append([]Parameter{Query("api_version", "Kubernetes API version", true), Boolean("cached", "Read saved objects")}, Paging()...)})
	calls := 0
	handler := c.Wrap("GET", "/resources", func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(204) })
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"?api_version=v1&cached=true&limit=2", 204},
		{"?resource=nodes", 400},
		{"?api_version=v1&cached=maybe", 400},
		{"?api_version=v1&limit=201", 400},
		{"?api_version=v1&offset=-1", 400},
		{"?api_version=v1&label_selector=x", 400},
	} {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest("GET", "/resources"+tc.query, nil))
		if w.Code != tc.want {
			t.Fatalf("%s: %d %s", tc.query, w.Code, w.Body.String())
		}
		if w.Code == 400 {
			if !json.Valid(w.Body.Bytes()) || w.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("invalid problem response: %s", w.Body.String())
			}
		}
	}
	if calls != 1 {
		t.Fatalf("invalid queries reached service: %d", calls)
	}
}
func TestPaginationEmptyAndPastEnd(t *testing.T) {
	for _, tc := range []struct {
		url               string
		total, start, end int
		more              bool
	}{
		{"/?limit=2&offset=1", 5, 1, 3, true},
		{"/?limit=2&offset=4", 5, 4, 5, false},
		{"/?limit=2&offset=100", 5, 5, 5, false},
		{"/", 0, 0, 0, false},
	} {
		p := Pagination(httptest.NewRequest("GET", tc.url, nil), tc.total)
		start, end := p.Bounds()
		if start != tc.start || end != tc.end || p.HasMore != tc.more {
			t.Fatalf("%s: %#v bounds %d %d", tc.url, p, start, end)
		}
		if p.HasMore && (p.NextOffset == nil || *p.NextOffset != end) {
			t.Fatalf("wrong next offset: %#v", p)
		}
	}
}
