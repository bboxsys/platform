package apitool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBoundedOversizedResultIsCompleteActionableJSON(t *testing.T) {
	calls := 0
	h := Bounded(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(strings.Repeat("x", DefaultMaxResponseBytes+10)))
	}), BoundedOptions{Guidance: func(path string) string { return "use " + path + "/summary" }})
	ctx := WithRunState(context.Background())
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/things", nil).WithContext(ctx))
		require.True(t, json.Valid(w.Body.Bytes()), "unusable error: %s", w.Body.String())
		require.Contains(t, w.Body.String(), "/api/v1/things/summary")
		require.Equal(t, "application/problem+json", w.Header().Get("Content-Type"))
		var p Problem
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &p))
		require.False(t, p.RetrySameRequest)
		want := http.StatusRequestEntityTooLarge
		if i == 2 {
			want = http.StatusConflict
		}
		require.Equal(t, want, w.Code, "attempt %d", i)
	}
	require.Equal(t, 2, calls, "repeated failing query reached the handler")
	// A new run has its own retry budget.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/things", nil).WithContext(WithRunState(context.Background())))
	require.Equal(t, 3, calls, "failure state leaked across runs")
}

func TestBoundedQueryChangesAndSuccessfulWritesResetFailureGuard(t *testing.T) {
	calls := 0
	h := Bounded(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}), BoundedOptions{})
	ctx := WithRunState(context.Background())
	call := func(method, target string) int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, target, nil).WithContext(ctx))
		return w.Code
	}
	call("GET", "/api/v1/things?a=1")
	call("GET", "/api/v1/things?a=1")
	require.Equal(t, http.StatusConflict, call("GET", "/api/v1/things?a=1"))
	require.Equal(t, http.StatusBadRequest, call("GET", "/api/v1/things?a=2"), "changed input must reach the handler")
	// The canonical query makes parameter order irrelevant.
	call("GET", "/api/v1/things?b=1&a=2")
	call("GET", "/api/v1/things?a=2&b=1")
	require.Equal(t, http.StatusConflict, call("GET", "/api/v1/things?a=2&b=1"))
	call("PUT", "/api/v1/things/one")
	require.Equal(t, http.StatusBadRequest, call("GET", "/api/v1/things?a=1"), "a successful write resets the budget")
}

func TestBoundedPassesSmallResponsesThrough(t *testing.T) {
	h := Bounded(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}), BoundedOptions{MaxResponseBytes: 64})
	// Without run state the body is still bounded; nothing else changes.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/things", nil))
	require.Equal(t, http.StatusCreated, w.Code)
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))
	require.JSONEq(t, `{"ok":true}`, w.Body.String())
}
