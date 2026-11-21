package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bboxsys/platform/ai"
	"github.com/bboxsys/platform/ai/instance"
	"github.com/bboxsys/platform/ai/memory"
	"github.com/bboxsys/platform/ai/usage"
	"github.com/bboxsys/platform/apicontract"
	"github.com/bboxsys/store"
	"github.com/dgraph-io/badger/v4"
)

const (
	// AIMountPrefix is where the assistant's routes live.
	AIMountPrefix = "/api/v1/ai"
	// AIToolPrefix prefixes every tool generated from the admin API.
	AIToolPrefix = "admin_"
	// AIDocumentsPurpose is the rag collection purpose documents are
	// ingested into.
	AIDocumentsPurpose = instance.DocumentsPurpose
	// MCPPath is the MCP endpoint. Clients send the account as the
	// accountID query parameter (the ID middleware reads it there) and the
	// user's bearer in the Authorization header.
	MCPPath = instance.MCPPath
	// AIDirName is the assistant's directory under AIOptions.Dir: its own
	// store at {Dir}/ai/ai.db and its index at {Dir}/ai/index.
	AIDirName = "ai"
)

// aiDeniedRoutes are the routes never offered to the model, as path
// segment prefixes ("*" matches any one segment, a template segment
// included). They cover the assistant itself, credential minting (a GET
// that creates a NATS user and returns its seed), sign-in and sign-out,
// OAuth and OIDC redirects, invitation tokens, the account JWT routes,
// and the unauthenticated bootstrap. Every operation tagged
// aiDeniedTag in the OpenAPI document is denied as well.
var aiDeniedRoutes = []string{
	AIMountPrefix,
	MCPPath,
	"/api/v1/servicecredentials",
	"/api/v1/login",
	"/api/v1/logout",
	"/api/v1/register",
	"/api/v1/users/*/token",
	"/api/v1/oauth",
	"/api/v1/oidc/*/authorize",
	"/api/v1/oidc/*/callback",
	"/api/v1/invitations/*",
	"/jwt",
	"/serviceendpoints",
}

// aiDeniedTag is the OpenAPI tag whose operations are never tools.
const aiDeniedTag = "Auth"

// AIOptions configures StartAI.
type AIOptions struct {
	// Dir is the admin's root directory; the assistant keeps its own store
	// and index under {Dir}/ai (see AIDirName). Required.
	Dir string
	// Providers maps ai.Credential.Provider values to factories. Empty
	// means the instance defaults: anthropic (with Effort and server-side
	// fallbacks), ollama, and openai_compat (with Effort).
	Providers map[string]ai.ProviderFactory
	// Effort is the reasoning effort passed to the default factories when
	// Providers is empty; "" lets each credential's ai_effort tag apply.
	// Ignored when Providers is set (the factory carries it).
	Effort string
	// System overrides the default system prompt when set.
	System string
	// Charts tells the model the client draws echarts fenced blocks as
	// charts (the admin SPA's Assistant page does). Ignored when System is
	// set.
	Charts bool
	// MaxTurns bounds the model turns of one run; zero means the loop's
	// default.
	MaxTurns int
	// AllowWrites lets the model run tools that are not read-only (every
	// non-GET API operation). Off by default. The memory tools, which
	// write only to the caller's own notes and facts, are allowed either way.
	// It also governs /mcp: without it the endpoint serves only read-only
	// tools and the memory tools.
	AllowWrites bool
	// Version is reported in usage heartbeats; empty means the platform
	// module's build version.
	Version string
}

// StartAI hosts the assistant as an ai/instance named AdminNATSService:
// its own store and index under {Dir}/ai, one tool per admin API
// operation dispatched through this server's own handler chain with the
// caller's bearer, credentials from the vault, routes under /api/v1/ai,
// and the MCP endpoint at /mcp. Usage goes straight into the AI usage
// store when InitAIUsageStore ran first (the admin is its own collector,
// so it publishes nothing over NATS). It is idempotent.
//
// Sessions and memory notes that an earlier version kept in admin.db are
// moved into the instance store the first time it starts.
//
// Routes are wrapped with the auth the server builds in AddHandlers,
// resolved per request, so StartAI may be called before or after
// AddHandlers (that is, before or after Start). cmd/admin calls it before
// Start so the router is not changed while it serves. The SPA fallback
// (ServeUI) is the router's NotFoundHandler, never a route, so it cannot
// shadow routes registered at any time.
func (adminServer *AdminServer) StartAI(opts AIOptions) error {
	if adminServer.Srv == nil || adminServer.Srv.Handler == nil {
		return errors.New("ai: admin server has no handler")
	}
	if opts.Dir == "" {
		return errors.New("ai: Dir is required")
	}
	adminServer.aiMu.Lock()
	defer adminServer.aiMu.Unlock()
	if adminServer.ai != nil {
		return nil
	}

	include, err := aiToolFilter(openapiSpec)
	if err != nil {
		return fmt.Errorf("ai: api tools: %w", err)
	}
	var local usage.Local
	if us := adminServer.aiUsageStore; us != nil {
		local = us
	}
	inst, err := instance.New(instance.Options{
		Name:        AdminNATSService,
		Dir:         filepath.Join(opts.Dir, AIDirName),
		Credentials: NewAICredentialResolver(adminServer.st),
		Providers:   opts.Providers,
		Effort:      opts.Effort,
		System:      opts.System,
		Charts:      opts.Charts,
		MaxTurns:    opts.MaxTurns,
		AllowWrites: opts.AllowWrites,
		OpenAPI:     openapiSpec,
		Handler:     adminServer.Srv.Handler,
		ToolPrefix:  AIToolPrefix,
		// Credential minting, sign-in flows, the assistant's own routes,
		// and the MCP endpoint are never offered to the model, as tools or
		// through api_request.
		ToolFilter: include,
		// Oversized or repeatedly failing reads are answered with the
		// admin API's own paging and filters (apitool.Bounded).
		ToolGuidance: aiToolGuidance,
		Local:        local,
		Version:      opts.Version,
		Logger:       adminServer.Logger,
	})
	if err != nil {
		return fmt.Errorf("ai: %w", err)
	}
	if n, err := migrateAIRecords(adminServer.st.Store, inst.Store()); err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("ai: moving sessions and notes out of admin.db failed")
	} else if n > 0 {
		adminServer.Logger.InfoE().Int("records", n).Msg("ai: moved sessions and notes from admin.db to the assistant store")
	}
	adminServer.ai = inst
	adminServer.aiOpts = opts

	adminAccountID := adminServer.AdminAccountID()
	bearer := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { adminServer.auth.Bearer(h)(w, r) }
	}
	requireAdmin := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { adminServer.auth.RequireAdmin(adminAccountID, h)(w, r) }
	}
	inst.Mount(adminServer.Router, AIMountPrefix, bearer, requireAdmin)

	adminServer.Logger.InfoE().Int("apiTools", len(inst.APITools())).Str("effort", opts.Effort).Bool("allowWrites", opts.AllowWrites).Bool("usageLocal", local != nil).Msg("ai: assistant started")
	// A gateway mounted first, with services already registered, gets its
	// All assistant now; one mounted later starts it on registration.
	if g := adminServer.AssistantGateway(); g != nil {
		if err := g.ensureAll(inst, opts); err != nil {
			adminServer.Logger.ErrorE().Err(err).Msg("ai: starting the All assistant")
		}
	}
	return nil
}

// aiStarted returns the hosted assistant and the options it was started
// with, or nil before StartAI.
func (adminServer *AdminServer) aiStarted() (*instance.Instance, AIOptions) {
	adminServer.aiMu.Lock()
	defer adminServer.aiMu.Unlock()
	return adminServer.ai, adminServer.aiOpts
}

// AIAllStore returns the All assistant's own store (for store metrics), or
// nil while it does not exist: before StartAI, without a mounted gateway,
// or until the first service assistant is registered.
func (adminServer *AdminServer) AIAllStore() *store.Store {
	if g := adminServer.AssistantGateway(); g != nil {
		return g.AllStore()
	}
	return nil
}

// AI returns the hosted assistant's service, or nil before StartAI.
func (adminServer *AdminServer) AI() *ai.Service {
	if inst := adminServer.AIInstance(); inst != nil {
		return inst.Service()
	}
	return nil
}

// AIInstance returns the hosted assistant, or nil before StartAI.
func (adminServer *AdminServer) AIInstance() *instance.Instance {
	adminServer.aiMu.Lock()
	defer adminServer.aiMu.Unlock()
	return adminServer.ai
}

// AIStore returns the assistant's own store (for store metrics), or nil
// before StartAI.
func (adminServer *AdminServer) AIStore() *store.Store {
	if inst := adminServer.AIInstance(); inst != nil {
		return inst.Store()
	}
	return nil
}

func (adminServer *AdminServer) closeAI() {
	adminServer.aiMu.Lock()
	inst := adminServer.ai
	adminServer.aiMu.Unlock()
	if inst != nil {
		if err := inst.Close(); err != nil {
			adminServer.Logger.ErrorE().Err(err).Msg("error closing ai instance")
		}
	}
}

// aiRecordPrefixes are the scoped prefixes ({accountID}:{prefix}:...) of
// the assistant records an earlier version kept in admin.db.
var aiRecordPrefixes = map[string]bool{ai.SessionPrefix: true, memory.NotesPrefix: true}

// migrateAIRecords moves every session and memory note from the admin
// store into the instance store, keeping keys, values, and expiry. It is a
// no-op once admin.db holds none.
func migrateAIRecords(from, to *store.Store) (int, error) {
	type kv struct {
		key, val  []byte
		expiresAt uint64
	}
	var found []kv
	err := from.DB.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			parts := strings.SplitN(string(item.Key()), ":", 3)
			if len(parts) < 3 || !aiRecordPrefixes[parts[1]] {
				continue
			}
			val, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			found = append(found, kv{key: item.KeyCopy(nil), val: val, expiresAt: item.ExpiresAt()})
		}
		return nil
	})
	if err != nil || len(found) == 0 {
		return 0, err
	}
	wb := to.DB.NewWriteBatch()
	for _, r := range found {
		e := badger.NewEntry(r.key, r.val)
		e.ExpiresAt = r.expiresAt
		if err := wb.SetEntry(e); err != nil {
			wb.Cancel()
			return 0, err
		}
	}
	if err := wb.Flush(); err != nil {
		return 0, err
	}
	keys := make([][]byte, 0, len(found))
	for _, r := range found {
		keys = append(keys, r.key)
	}
	if err := from.DeleteKeys(keys); err != nil {
		return len(found), fmt.Errorf("copied but not deleted: %w", err)
	}
	return len(found), nil
}

// aiToolGuidance names, for a request path, the way to narrow an admin
// read that overflowed the tool budget or keeps failing: the list routes
// page with limit and offset (writeList), and the rolling stores filter.
func aiToolGuidance(path string) string {
	switch {
	case strings.HasPrefix(path, "/api/v1/logs"):
		return "Narrow the query with level, service, host, search, since or until, and page with limit and offset. GET /api/v1/logs/count and /api/v1/logs/summary answer counts without entries."
	case strings.HasPrefix(path, "/api/v1/metrics/stores/history"):
		return "Narrow with store_name or service and page with limit and offset. GET /api/v1/metrics/stores answers the current values without history."
	case strings.HasPrefix(path, "/api/v1/ai/usage/runs"):
		return "Filter by instance, host, account_id or model, and page with limit and offset. GET /api/v1/ai/usage/summary answers totals without runs."
	}
	return "Page the list with limit (at most " + strconv.Itoa(apicontract.MaxPageLimit) + ") and offset; the page object carries total, and next_offset while has_more is true. GET /api/v1/accounts/stats answers per-account counts without rows."
}

// aiToolFilter returns the Include filter for the admin API tools: it
// rejects every route in aiDeniedRoutes and every operation the document
// tags aiDeniedTag. It accepts a path template or a concrete path, since
// the request tool calls it with the latter.
func aiToolFilter(spec []byte) (func(method, path string) bool, error) {
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(spec, &doc); err != nil {
		return nil, fmt.Errorf("parse OpenAPI document: %w", err)
	}
	type tagged struct{ method, path string }
	var byTag []tagged
	for p, item := range doc.Paths {
		for m, raw := range item {
			var op struct {
				Tags []string `json:"tags"`
			}
			if json.Unmarshal(raw, &op) != nil {
				continue
			}
			for _, tag := range op.Tags {
				if tag == aiDeniedTag {
					byTag = append(byTag, tagged{strings.ToUpper(m), p})
				}
			}
		}
	}
	return func(method, path string) bool {
		for _, prefix := range aiDeniedRoutes {
			if instance.PathHasPrefix(path, prefix) {
				return false
			}
		}
		for _, t := range byTag {
			if t.method == method && instance.PathMatches(path, t.path) {
				return false
			}
		}
		return true
	}, nil
}
