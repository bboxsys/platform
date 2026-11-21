package instance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/bboxsys/platform/ai"
	"github.com/bboxsys/platform/ai/apitool"
	"github.com/bboxsys/platform/ai/mcp"
	"github.com/bboxsys/platform/ai/rag"
	"github.com/gorilla/mux"
)

// Mount registers the instance's routes on r:
//
//	{prefix}/status, /sessions, ...          ai.Service routes, bearer
//	POST {prefix}/documents                  AddDocuments, requireAdmin
//	GET  {prefix}/documents/search           SearchDocuments, bearer
//	POST {prefix}/credentials/test           TestCredential, bearer
//	POST {prefix}/credentials/models         ListModels, bearer
//	MCPPath                                  ServeMCP, bearer
//
// prefix "" means ai.DefaultMountPrefix. bearer is the host's bearer
// middleware (it must put the caller's account and user in the context the
// way the platform's auth does); requireAdmin wraps the administrator-only
// route and defaults to bearer when nil. A nil bearer registers the routes
// unwrapped, which only suits tests. Every route also gets a fresh
// apitool.WithRunState, so the API tools' failure guard is scoped to the
// request that starts the run. Mount once per instance.
func (i *Instance) Mount(r *mux.Router, prefix string, bearer, requireAdmin func(http.HandlerFunc) http.HandlerFunc) {
	if prefix == "" {
		prefix = ai.DefaultMountPrefix
	}
	prefix = strings.TrimRight(prefix, "/")
	if bearer == nil {
		bearer = func(h http.HandlerFunc) http.HandlerFunc { return h }
	}
	if requireAdmin == nil {
		requireAdmin = bearer
	}
	bearer, requireAdmin = withRunState(bearer), withRunState(requireAdmin)
	i.mountMu.Lock()
	i.mountPrefix = prefix
	i.mountMu.Unlock()
	i.service.Mount(r, prefix, bearer)
	r.HandleFunc(prefix+"/documents", requireAdmin(i.AddDocuments)).Methods("POST")
	r.HandleFunc(prefix+"/documents/search", bearer(i.SearchDocuments)).Methods("GET")
	r.HandleFunc(prefix+"/credentials/test", bearer(i.TestCredential)).Methods("POST")
	r.HandleFunc(prefix+"/credentials/models", bearer(i.ListModels)).Methods("POST")
	r.Handle(MCPPath, bearer(i.ServeMCP))
}

// withRunState gives each request its own API tool failure guard.
func withRunState(wrap func(http.HandlerFunc) http.HandlerFunc) func(http.HandlerFunc) http.HandlerFunc {
	return func(h http.HandlerFunc) http.HandlerFunc {
		return wrap(func(w http.ResponseWriter, r *http.Request) {
			h(w, r.WithContext(apitool.WithRunState(r.Context())))
		})
	}
}

// toolsKey carries a request's tool set to the MCP handler's get func.
type toolsKey struct{}

func newMCPHandler(name string) http.Handler {
	return mcp.HandlerWithInfo(name, mcp.DefaultServerVersion, func(r *http.Request) ([]ai.Tool, error) {
		tools, ok := r.Context().Value(toolsKey{}).([]ai.Tool)
		if !ok {
			return nil, errors.New("no tool set for this request")
		}
		return tools, nil
	})
}

// ServeMCP serves the MCP endpoint: the caller's tool set is built from
// the request, placed in the context with the caller, served, and closed
// when the request ends. An MCP client applies its own approval, so the
// assistant's write policy is applied here instead: without AllowWrites
// only read-only tools and the memory tools are served.
func (i *Instance) ServeMCP(w http.ResponseWriter, r *http.Request) {
	caller := ai.CallerFromRequest(r)
	if caller.AccountID == "" || caller.UserID == "" {
		http.Error(w, `{"error":"no caller identity"}`, http.StatusUnauthorized)
		return
	}
	ctx := ai.WithCaller(r.Context(), caller)
	tools, err := i.Tools(ctx, caller)
	if err != nil {
		i.log.ErrorE().Err(err).Msg("mcp: building tools failed")
		http.Error(w, `{"error":"building the tool set failed"}`, http.StatusInternalServerError)
		return
	}
	defer CloseTools(tools)
	if !i.opts.AllowWrites {
		served := tools[:0:0]
		for _, t := range tools {
			if t.ReadOnly() || i.allowTools[t.Name()] {
				served = append(served, t)
			}
		}
		tools = served
	}
	ctx = context.WithValue(ctx, toolsKey{}, tools)
	i.mcp.ServeHTTP(w, r.WithContext(ctx))
}

// SearchResult is rag.Result on the wire.
type SearchResult struct {
	ID         string            `json:"id"`
	Content    string            `json:"content"`
	Metadata   map[string]string `json:"metadata"`
	Similarity float32           `json:"similarity"`
}

// Document is one document to ingest.
type Document struct {
	ID       string            `json:"id"`
	Content  string            `json:"content"`
	Metadata map[string]string `json:"metadata"`
}

// AddDocuments ingests documents into the account's collection:
// POST {prefix}/documents {documents: [{id, content, metadata}]}. Answers
// 413 when the body exceeds MaxDocumentsBody and 412 when the caller has
// no embedding credential.
func (i *Instance) AddDocuments(w http.ResponseWriter, r *http.Request) {
	caller := ai.CallerFromRequest(r)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxDocumentsBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("body exceeds %d bytes", MaxDocumentsBody))
			return
		}
		writeError(w, http.StatusBadRequest, "reading the body failed")
		return
	}
	embed, ok := i.embedder(r.Context(), caller)
	if !ok {
		writeError(w, http.StatusPreconditionFailed, "no AI embedding credential configured")
		return
	}
	var in struct {
		Documents []Document `json:"documents"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(in.Documents) == 0 {
		writeError(w, http.StatusBadRequest, "documents is required")
		return
	}
	docs := make([]rag.Document, 0, len(in.Documents))
	for _, d := range in.Documents {
		if strings.TrimSpace(d.ID) == "" || strings.TrimSpace(d.Content) == "" {
			writeError(w, http.StatusBadRequest, "every document needs an id and content")
			return
		}
		docs = append(docs, rag.Document{ID: d.ID, Content: d.Content, Metadata: d.Metadata})
	}
	col, err := i.index.Collection(caller.AccountID, DocumentsPurpose, embed)
	if err != nil {
		i.log.ErrorE().Err(err).Msg("ai: documents collection")
		writeError(w, http.StatusInternalServerError, "opening the collection failed")
		return
	}
	if err := rag.Add(r.Context(), col, docs); err != nil {
		i.log.ErrorE().Err(err).Msg("ai: add documents")
		writeError(w, http.StatusBadGateway, "indexing failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"added": len(docs)})
}

// SearchDocuments queries the account's collection:
// GET {prefix}/documents/search?q=&n=. Answers 412 when the caller has no
// embedding credential.
func (i *Instance) SearchDocuments(w http.ResponseWriter, r *http.Request) {
	caller := ai.CallerFromRequest(r)
	embed, ok := i.embedder(r.Context(), caller)
	if !ok {
		writeError(w, http.StatusPreconditionFailed, "no AI embedding credential configured")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeError(w, http.StatusBadRequest, "q is required")
		return
	}
	n := 5
	if v := r.URL.Query().Get("n"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed <= 0 || parsed > 50 {
			writeError(w, http.StatusBadRequest, "n must be between 1 and 50")
			return
		}
		n = parsed
	}
	col, err := i.index.Collection(caller.AccountID, DocumentsPurpose, embed)
	if err != nil {
		i.log.ErrorE().Err(err).Msg("ai: documents collection")
		writeError(w, http.StatusInternalServerError, "opening the collection failed")
		return
	}
	hits, err := rag.Query(r.Context(), col, q, n, nil)
	if err != nil {
		i.log.ErrorE().Err(err).Msg("ai: query documents")
		writeError(w, http.StatusBadGateway, "search failed: "+err.Error())
		return
	}
	out := make([]SearchResult, 0, len(hits))
	for _, h := range hits {
		meta := h.Metadata
		if meta == nil {
			meta = map[string]string{}
		}
		out = append(out, SearchResult{ID: h.ID, Content: h.Content, Metadata: meta, Similarity: h.Similarity})
	}
	writeJSON(w, http.StatusOK, out)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
