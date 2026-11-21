// Package instance hosts one AI assistant with reasonable defaults: its own
// store, its own retrieval index, memory, tools generated from the host's
// OpenAPI document, remote MCP servers, the credential test, the documents
// routes, the MCP endpoint, and usage reporting. Every host of package ai
// (the admin server, a product service, a host agent, an app) goes through
// it, so adding an assistant is one call:
//
//	inst, err := instance.New(instance.Options{
//		Name:        "inventory_service",
//		Dir:         filepath.Join(root, "ai"),
//		Credentials: resolver,
//		OpenAPI:     spec,
//		Handler:     srv.Srv.Handler,
//		ToolPrefix:  "inventory_",
//		Publisher:   srv.MessengerClient,
//	})
//	inst.Mount(srv.Router, "/api/v1/ai", bearer, requireAdmin)
//	defer inst.Close()
//
// A service built on package server can use serverhost.New (package
// ai/instance/serverhost), which fills Name, Handler, Publisher, and Logger
// from the server. It is a separate package so this one does not link
// package server into hosts that do not run it (a host agent, a CLI).
//
// # Store layout
//
// Everything an instance keeps lives under Dir: {Dir}/ai.db is its own
// Badger store (sessions, memory notes, usage records and their outbox) and
// {Dir}/index is its retrieval index (documents and remembered facts). No
// instance writes into another store, so sessions never land in the
// host's main database (for the admin, never in admin.db). Collections of
// the earlier layout, where the index was Dir itself, are moved into
// {Dir}/index the first time New finds no index there.
//
// # Credentials
//
// Credentials resolves the caller's model, embedding, and MCP server
// credentials. The admin server reads them from its vault
// (admin.NewAICredentialResolver). A host without the vault, a host agent
// or a product that reads a key from its own configuration, uses
// ai.StaticCredentials, or a resolver that asks the admin server over
// natsapi (GET /api/v1/apitokens with the agent's own JWT as the bearer).
//
// # Usage reporting
//
// Every instance records each run in its own store for 30 days
// (usage.DefaultRetention) and, when a Publisher is set (normally the
// host's *messenger.Client), publishes it on sys.ai.usage.{Name} and a
// heartbeat with running totals every 60 s on sys.ai.heartbeat.{Name}.
// Records that could not be published wait in an outbox and are retried
// with each heartbeat. The admin server's collectors subscribe to
// sys.ai.> in every account; the admin's own instance hands its records to
// the collector store in-process through Local instead.
package instance

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/bboxsys/platform/ai"
	"github.com/bboxsys/platform/ai/anthropic"
	"github.com/bboxsys/platform/ai/apitool"
	"github.com/bboxsys/platform/ai/docsearch"
	"github.com/bboxsys/platform/ai/memory"
	"github.com/bboxsys/platform/ai/openai"
	"github.com/bboxsys/platform/ai/rag"
	"github.com/bboxsys/platform/ai/usage"
	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/store"
)

const (
	// StoreName is the instance store's directory under Dir.
	StoreName = "ai.db"
	// IndexName is the retrieval index's directory under Dir.
	IndexName = "index"
	// MCPPath is where Mount serves the MCP endpoint. Clients send the
	// account as the accountID query parameter (the ID middleware reads it
	// there) and the user's bearer in the Authorization header.
	MCPPath = "/mcp"
	// DocumentsPurpose is the rag collection purpose documents are
	// ingested into.
	DocumentsPurpose = "documents"
	// MaxDocumentsBody bounds one ingestion request.
	MaxDocumentsBody = 16 << 20
	// MCPConnectTimeout bounds connecting to one remote MCP server and
	// listing its tools while a tool set is built.
	MCPConnectTimeout = 15 * time.Second
	// CheckTimeout bounds one credential test, so a dead endpoint answers
	// in seconds instead of holding the request.
	CheckTimeout = 20 * time.Second
	// PlatformModule is the module whose version is reported by default.
	PlatformModule = "github.com/bboxsys/platform"
)

// Options configures an Instance.
type Options struct {
	// Name names the instance; it is the last token of the usage subjects
	// and the MCP server name, so it must match [A-Za-z0-9_-]+. Required,
	// for example "admin_service".
	Name string
	// Service is the component shown in usage; default Name.
	Service string
	// Dir holds the instance's own store ({Dir}/ai.db) and index
	// ({Dir}/index). Required.
	Dir string
	// Credentials resolves the caller's credentials. Required.
	Credentials ai.CredentialResolver
	// Providers maps ai.Credential.Provider values to factories. Default:
	// anthropic (with Effort and server-side fallbacks), ollama, and
	// openai_compat (both ai/openai, with Effort).
	Providers map[string]ai.ProviderFactory
	// Effort is passed to the default factories only; "" lets each
	// credential's ai_effort tag apply.
	Effort string
	// System overrides the default system prompt when set.
	System string
	// Instructions are appended to the system prompt (the default or
	// System), one block each: a host's standing guidance that does not
	// replace the platform rules. Optional.
	Instructions []string
	// Docs are the documentation corpora the assistant can search and
	// cite: each adds its {ToolPrefix}docs_search and docs_read tools and
	// its guidance to the system prompt. The host builds, starts and
	// closes them (after the instance). Optional.
	Docs []*docsearch.Corpus
	// Charts reports that the client renders echarts fenced blocks as
	// charts; the default system prompt then says how to draw one. See
	// ai.ServiceOptions.Charts.
	Charts bool
	// MaxTurns bounds the model turns of one run; zero means the loop's
	// default.
	MaxTurns int
	// SetupTimeout bounds building a run's tool set and preamble; zero
	// means ai.DefaultSetupTimeout.
	SetupTimeout time.Duration
	// AllowWrites lets the model run tools that are not read-only. Off by
	// default; the memory tools, which write only to the caller's own notes
	// and facts, are allowed either way. It also governs MCPPath, whose
	// client applies its own approval: without it only read-only tools and
	// the memory tools are served there.
	AllowWrites bool

	// OpenAPI is the host's OpenAPI document; with Handler it yields one
	// tool per operation plus a generic {ToolPrefix}api_request tool, each
	// dispatching through Handler with the caller's bearer. Optional.
	OpenAPI []byte
	// Handler is the host's full handler chain (ID middleware included).
	Handler http.Handler
	// ToolPrefix prefixes every generated tool name, for example "admin_".
	ToolPrefix string
	// ToolFilter rejects operations (method upper case, path template or
	// concrete path) that must never be tools. The instance's own routes
	// (the mount prefix, ai.DefaultMountPrefix, and MCPPath) are always
	// rejected on top of it.
	ToolFilter func(method, path string) bool
	// MaxToolResponseBytes is the response budget of every API tool call
	// (apitool.Bounded): a larger response is replaced by a complete
	// problem document naming a compact query, never a truncated object
	// list. Zero means apitool.DefaultMaxResponseBytes (32 KiB), which
	// stays below the agent's tool-result limit with the envelope added.
	MaxToolResponseBytes int
	// ToolGuidance returns, for a request path, the compact or paginated
	// query the model should use when a response overflowed or an
	// identical read keeps failing. Nil means apitool.DefaultGuidance; a
	// product names its own summary routes here.
	ToolGuidance func(path string) string
	// Tools returns additional tools for one caller, built per run after
	// the generated API tools and before memory, search and the remote MCP
	// servers: a host whose tools are not an HTTP API (the admin's "All"
	// assistant delegating to the service assistants, for example) supplies
	// them here. A tool whose name is already taken is skipped and logged.
	// Optional.
	Tools func(ctx context.Context, c ai.Caller) ([]ai.Tool, error)

	// Publisher sends usage records and heartbeats over NATS, usually the
	// host's *messenger.Client. Optional; without it records stay in the
	// instance's outbox.
	Publisher usage.Publisher
	// Local receives usage in-process, for a host that is itself the
	// collector (the admin's AIUsageStore). Optional.
	Local usage.Local
	// UsageRetention is how long the instance keeps its own records; zero
	// means usage.DefaultRetention (30 days).
	UsageRetention time.Duration
	// HeartbeatInterval is the usage heartbeat period; zero means
	// usage.DefaultHeartbeatInterval (60 s).
	HeartbeatInterval time.Duration
	// Version is reported in heartbeats; empty means the platform module's
	// build version.
	Version string

	// Logger receives operational lines; nil logs as ai/{Name}.
	Logger *logger.Logger
}

// Instance is one hosted assistant.
type Instance struct {
	opts      Options
	st        *store.Store
	index     *rag.Index
	notes     *memory.Notes
	recorder  *usage.Recorder
	service   *ai.Service
	apiTools  []ai.Tool
	reqTool   ai.Tool
	mcp       http.Handler
	log       *logger.Logger
	providers map[string]ai.ProviderFactory
	// allowTools mirrors the service's AllowTools for MCPPath.
	allowTools map[string]bool

	mountMu     sync.RWMutex
	mountPrefix string

	closeOnce sync.Once
	closeErr  error
}

// memoryTools are allowed without AllowWrites: their writes stay inside the
// caller's own notes and facts.
var memoryTools = []string{memory.NotesToolName, memory.RememberToolName}

// New opens the instance's store and index and builds its service, tools,
// and usage recorder, which it starts.
func New(o Options) (*Instance, error) {
	if o.Name == "" {
		return nil, errors.New("ai/instance: Name is required")
	}
	if o.Dir == "" {
		return nil, errors.New("ai/instance: Dir is required")
	}
	if o.Credentials == nil {
		return nil, errors.New("ai/instance: Credentials is required")
	}
	if o.OpenAPI != nil && o.Handler == nil {
		return nil, errors.New("ai/instance: OpenAPI needs a Handler to dispatch to")
	}
	if o.Service == "" {
		o.Service = o.Name
	}
	if o.Logger == nil {
		o.Logger = logger.NewLogger("ai", o.Name)
	}
	if o.Version == "" {
		o.Version = PlatformVersion()
	}
	if isNil(o.Publisher) {
		o.Publisher = nil
	}
	if isNil(o.Local) {
		o.Local = nil
	}
	providers := o.Providers
	if len(providers) == 0 {
		providers = map[string]ai.ProviderFactory{
			ai.ProviderAnthropic:    anthropic.Factory(anthropic.Options{Effort: o.Effort, Fallbacks: true, Logger: o.Logger}),
			ai.ProviderOllama:       openai.OllamaFactory(openai.Options{Effort: o.Effort, Logger: o.Logger}),
			ai.ProviderOpenAICompat: openai.Factory(openai.Options{Effort: o.Effort, Logger: o.Logger}),
		}
	}
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("ai/instance: create %s: %w", o.Dir, err)
	}

	i := &Instance{
		opts:       o,
		log:        o.Logger,
		providers:  providers,
		allowTools: make(map[string]bool, len(memoryTools)),
	}
	for _, name := range memoryTools {
		i.allowTools[name] = true
	}
	ok := false
	defer func() {
		if !ok {
			i.Close()
		}
	}()

	var err error
	i.st, err = store.NewStore(&store.Options{
		Location:    filepath.Join(o.Dir, StoreName),
		Application: "ai",
		Component:   o.Name,
	})
	if err != nil {
		return nil, fmt.Errorf("ai/instance: open store: %w", err)
	}
	i.notes = memory.NewNotes(i.st)
	indexDir := filepath.Join(o.Dir, IndexName)
	if moved := migrateLegacyIndex(o.Dir, indexDir, i.log); moved > 0 {
		i.log.InfoE().Int("collections", moved).Str("index", indexDir).Msg("ai: moved index collections from the earlier layout")
	}
	i.index, err = rag.Open(indexDir)
	if err != nil {
		return nil, fmt.Errorf("ai/instance: open index: %w", err)
	}

	if o.Handler != nil && o.OpenAPI != nil {
		// Every tool call goes through the bounded handler, so the model
		// reads complete documents and a failing read is not repeated
		// within a run (the run state is installed by Mount).
		handler := apitool.Bounded(o.Handler, apitool.BoundedOptions{MaxResponseBytes: o.MaxToolResponseBytes, Guidance: o.ToolGuidance})
		apiOpts := apitool.Options{Prefix: o.ToolPrefix, Include: i.include}
		i.apiTools, err = apitool.FromOpenAPI(o.OpenAPI, handler, apiOpts)
		if err != nil {
			return nil, fmt.Errorf("ai/instance: api tools: %w", err)
		}
		i.reqTool = apitool.NewRequestTool(handler, apiOpts)
	}

	provider, model := defaultModel(o.Credentials, providers)
	i.recorder, err = usage.NewRecorder(usage.RecorderOptions{
		Instance:          o.Name,
		Service:           o.Service,
		Version:           o.Version,
		Store:             i.st,
		Publisher:         o.Publisher,
		Local:             o.Local,
		Retention:         o.UsageRetention,
		HeartbeatInterval: o.HeartbeatInterval,
		Provider:          provider,
		Model:             model,
		Logger:            o.Logger,
	})
	if err != nil {
		return nil, fmt.Errorf("ai/instance: %w", err)
	}

	svcOpts := ai.ServiceOptions{
		Credentials:  o.Credentials,
		Providers:    providers,
		Sessions:     ai.NewStoreSessions(i.st),
		Tools:        i.Tools,
		Preamble:     i.preamble,
		MaxTurns:     o.MaxTurns,
		SetupTimeout: o.SetupTimeout,
		Charts:       o.Charts,
		AllowWrites:  o.AllowWrites,
		AllowTools:   memoryTools,
		Usage:        i.recorder,
		Logger:       o.Logger,
	}
	extras := append([]string(nil), o.Instructions...)
	for _, corpus := range o.Docs {
		if corpus != nil {
			extras = append(extras, corpus.Guidance(o.ToolPrefix))
		}
	}
	if o.System != "" || len(extras) > 0 {
		base, charts := o.System, o.Charts
		svcOpts.System = func(_ context.Context, c ai.Caller) string {
			system := base
			if system == "" {
				system = ai.DefaultSystem(c)
				if charts {
					system += "\n" + ai.ChartGuidance
				}
			}
			for _, e := range extras {
				system += "\n" + strings.TrimSpace(e) + "\n"
			}
			return system
		}
	}
	i.service, err = ai.NewService(svcOpts)
	if err != nil {
		return nil, err
	}
	i.mcp = newMCPHandler(o.Name)
	i.recorder.Start()
	ok = true
	return i, nil
}

// Options returns the options with their defaults applied.
func (i *Instance) Options() Options { return i.opts }

// Service returns the assistant's HTTP surface.
func (i *Instance) Service() *ai.Service { return i.service }

// Store returns the instance's own store, for store metrics.
func (i *Instance) Store() *store.Store { return i.st }

// Index returns the instance's retrieval index.
func (i *Instance) Index() *rag.Index { return i.index }

// Usage returns the usage recorder.
func (i *Instance) Usage() *usage.Recorder { return i.recorder }

// APITools returns the tools generated from the OpenAPI document.
func (i *Instance) APITools() []ai.Tool { return i.apiTools }

// RequestTool returns the generic api_request tool, or nil without an
// OpenAPI document.
func (i *Instance) RequestTool() ai.Tool { return i.reqTool }

// Close stops the usage recorder (sending a final heartbeat) and closes
// the index and the store. It is idempotent.
func (i *Instance) Close() error {
	i.closeOnce.Do(func() {
		var errs []error
		if i.recorder != nil {
			errs = append(errs, i.recorder.Close())
		}
		if i.index != nil {
			errs = append(errs, i.index.Close())
		}
		if i.st != nil {
			errs = append(errs, i.st.Close())
		}
		i.closeErr = errors.Join(errs...)
	})
	return i.closeErr
}

// include is the apitool filter: the host's ToolFilter plus the
// instance's own routes.
func (i *Instance) include(method, path string) bool {
	i.mountMu.RLock()
	prefix := i.mountPrefix
	i.mountMu.RUnlock()
	for _, own := range []string{ai.DefaultMountPrefix, prefix, MCPPath} {
		if own != "" && PathHasPrefix(path, own) {
			return false
		}
	}
	return i.opts.ToolFilter == nil || i.opts.ToolFilter(method, path)
}

// defaultModel reports the provider and model of the instance's default
// credential when that is known without a caller, as with
// ai.StaticCredentials; otherwise the single configured provider, if any.
func defaultModel(creds ai.CredentialResolver, providers map[string]ai.ProviderFactory) (string, string) {
	var sc *ai.StaticCredentials
	switch c := creds.(type) {
	case ai.StaticCredentials:
		sc = &c
	case *ai.StaticCredentials:
		sc = c
	}
	if sc != nil && sc.ModelCredential != nil {
		p := sc.ModelCredential.Provider
		if p == "" {
			p = ai.ProviderAnthropic
		}
		return p, sc.ModelCredential.Model
	}
	if len(providers) == 1 {
		for name := range providers {
			return name, ""
		}
	}
	return "", ""
}

// PlatformVersion is the platform module's version in this binary's build
// information ("(devel)" in a workspace build), or "".
func PlatformVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	if bi.Main.Path == PlatformModule {
		return bi.Main.Version
	}
	for _, d := range bi.Deps {
		if d.Path == PlatformModule {
			if d.Replace != nil && d.Replace.Version != "" {
				return d.Replace.Version
			}
			return d.Version
		}
	}
	return ""
}

// migrateLegacyIndex moves collection directories that an earlier layout
// kept directly in dir into indexDir, once: only while indexDir does not
// exist yet. A collection directory is one holding chromem's metadata file
// (00000000.gob, compressed or not). Failures are logged and skipped; the
// collection then stays where it was, unused.
func migrateLegacyIndex(dir, indexDir string, l *logger.Logger) int {
	if _, err := os.Stat(indexDir); err == nil || !errors.Is(err, os.ErrNotExist) {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	moved := 0
	for _, e := range entries {
		if !e.IsDir() || e.Name() == StoreName || e.Name() == IndexName {
			continue
		}
		src := filepath.Join(dir, e.Name())
		if !isCollectionDir(src) {
			continue
		}
		if err := os.MkdirAll(indexDir, 0o700); err != nil {
			l.WarnE().Err(err).Msg("ai: create index directory for migration")
			return moved
		}
		if err := os.Rename(src, filepath.Join(indexDir, e.Name())); err != nil {
			l.WarnE().Err(err).Str("collection", src).Msg("ai: move index collection")
			continue
		}
		moved++
	}
	return moved
}

func isCollectionDir(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "00000000.gob") {
			return true
		}
	}
	return false
}

// isNil reports whether v is nil or an interface holding a nil pointer, so
// a typed nil (a nil *messenger.Client) is treated as absent.
func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Interface, reflect.Chan:
		return rv.IsNil()
	}
	return false
}
