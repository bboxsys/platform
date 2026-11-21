package apitool

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
)

const (
	// DefaultMaxResponseBytes is the response budget of Bounded. It stays
	// well below ai.DefaultMaxToolResultBytes (64 KiB) so every response
	// the model reads is complete JSON once the result envelope is added,
	// including the problem document that replaces an oversized body.
	DefaultMaxResponseBytes = 32 * 1024
	// DefaultMaxRepeatedFailures is how often one identical failed GET
	// reaches the handler within a run before Bounded answers for it.
	DefaultMaxRepeatedFailures = 2
	// DefaultGuidance is the advice in a problem document when the host
	// supplies no Guidance.
	DefaultGuidance = "Use a documented summary or paginated query. Report missing evidence if no bounded query is available."
)

// BoundedOptions configures Bounded.
type BoundedOptions struct {
	// MaxResponseBytes is the largest body a tool call may return. Zero
	// means DefaultMaxResponseBytes.
	MaxResponseBytes int
	// MaxRepeatedFailures is how many times one identical GET (method,
	// path and canonical query) may fail or overflow within a run before
	// the handler is no longer called for it. Zero means
	// DefaultMaxRepeatedFailures.
	MaxRepeatedFailures int
	// Guidance returns the advice placed in a problem document for a
	// request path: which compact or paginated query the model should use
	// instead. Nil means DefaultGuidance for every path. A product supplies
	// its own summary routes here; the platform names none.
	Guidance func(path string) string
}

func (o BoundedOptions) withDefaults() BoundedOptions {
	if o.MaxResponseBytes <= 0 {
		o.MaxResponseBytes = DefaultMaxResponseBytes
	}
	if o.MaxRepeatedFailures <= 0 {
		o.MaxRepeatedFailures = DefaultMaxRepeatedFailures
	}
	if o.Guidance == nil {
		o.Guidance = func(string) string { return DefaultGuidance }
	}
	return o
}

// runStateKey carries a run's failure counts in its context.
type runStateKey struct{}

// runState counts identical failed reads within one run.
type runState struct {
	mu       sync.Mutex
	failures map[string]int
}

// WithRunState returns a context carrying a fresh failure guard for one
// run. A host installs it on the request that starts a run (ai/instance
// does so in Mount); tool calls inherit it through the run context, and
// state never leaks between runs. Without it Bounded only bounds the body.
func WithRunState(ctx context.Context) context.Context {
	return context.WithValue(ctx, runStateKey{}, &runState{failures: map[string]int{}})
}

// boundedResponse captures a handler's response up to the budget.
type boundedResponse struct {
	header    http.Header
	status    int
	body      bytes.Buffer
	limit     int
	oversized bool
}

func (w *boundedResponse) Header() http.Header { return w.header }
func (w *boundedResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *boundedResponse) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n := len(data)
	remaining := w.limit - w.body.Len()
	if len(data) > remaining {
		w.oversized = true
		data = data[:remaining]
	}
	_, _ = w.body.Write(data)
	return n, nil
}
func (w *boundedResponse) Flush() {}

// Problem is the RFC 9457 document Bounded answers with. RetrySameRequest
// is always false: the same call will not succeed.
type Problem struct {
	Type             string `json:"type"`
	Title            string `json:"title"`
	Status           int    `json:"status"`
	Detail           string `json:"detail"`
	RetrySameRequest bool   `json:"retry_same_request"`
}

func writeProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{Type: "about:blank", Title: title, Status: status, Detail: detail})
}

// Bounded wraps the handler API tools dispatch through so the model only
// ever reads complete documents. A response larger than the budget is
// replaced by a 413 problem document that names a compact query instead
// of a truncated object list, and an identical GET that failed or
// overflowed MaxRepeatedFailures times in one run (see WithRunState) is
// answered with a 409 problem without reaching the handler again. A
// successful mutation clears the run's failure counts, because the data
// behind the failed reads may have changed. Streaming handlers must be
// excluded from the tool surface before they reach this wrapper.
func Bounded(next http.Handler, o BoundedOptions) http.Handler {
	o = o.withDefaults()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state, _ := r.Context().Value(runStateKey{}).(*runState)
		key := r.Method + " " + r.URL.Path + "?" + r.URL.Query().Encode()
		if state != nil && r.Method == http.MethodGet {
			state.mu.Lock()
			failures := state.failures[key]
			state.mu.Unlock()
			if failures >= o.MaxRepeatedFailures {
				writeProblem(w, http.StatusConflict, "This identical query has already failed in this run; change the query or finish with the available evidence", o.Guidance(r.URL.Path))
				return
			}
		}
		response := &boundedResponse{header: http.Header{}, limit: o.MaxResponseBytes}
		next.ServeHTTP(response, r)
		if response.status == 0 {
			response.status = http.StatusOK
		}
		if state != nil {
			state.mu.Lock()
			switch {
			case r.Method != http.MethodGet && response.status < 400:
				clear(state.failures)
			case response.status >= 400 || response.oversized:
				state.failures[key]++
			default:
				delete(state.failures, key)
			}
			state.mu.Unlock()
		}
		if response.oversized {
			writeProblem(w, http.StatusRequestEntityTooLarge, "API result exceeds the tool response budget; no partial object list was returned", o.Guidance(r.URL.Path))
			return
		}
		for name, values := range response.header {
			w.Header()[name] = values
		}
		w.WriteHeader(response.status)
		_, _ = w.Write(response.body.Bytes())
	})
}
