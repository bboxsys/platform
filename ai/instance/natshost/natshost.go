// Package natshost serves an AI instance's conversation API over NATS
// (package natsapi) instead of a listener, for a product service whose
// assistant is reached through the admin console's gateway
// (admin.AssistantGateway) rather than by the browser directly. The host
// adds what a relayed conversation needs on top of ai/instance:
//
//   - request identity: the gateway names each request in RequestHeader,
//     scoped to the caller's account and user, so a second request with the
//     same id is refused (409) and a cancel names exactly one run;
//   - cancellation: POST {prefix}/requests/{id}/cancel ends the run, also
//     when it arrives before the request itself was dispatched (NATS
//     handlers run concurrently), and waits briefly for the run to end;
//   - a bounded run duration (MaxRunDuration);
//   - account removal (Stop) and an orderly Close that cancels and joins
//     every active run before the instance's stores close.
//
// Model credentials never travel over NATS: the host resolves them in
// process through Options.Instance.Credentials, as any instance does.
//
//	host, err := natshost.New(natshost.Options{
//	    Instance: instance.Options{Name: "inventory_service", Dir: dir, Credentials: resolver,
//	        OpenAPI: spec, Handler: srv.Srv.Handler, ToolFilter: include, ToolPrefix: "inventory_"},
//	    Auth: auth, AdminAccountID: adminAccount,
//	})
//	gateway.Register(admin.AssistantService{ID: "inventory", Name: "Inventory", Serve: host.Serve, Stop: host.Stop})
//	defer host.Close()
package natshost

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	admin "github.com/bboxsys/platform/admin/client"
	"github.com/bboxsys/platform/ai"
	"github.com/bboxsys/platform/ai/instance"
	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/platform/natsapi"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/nats-io/nats.go"
)

const (
	// RequestHeader names a conversation request (a UUID) so it can be
	// cancelled. The gateway sets it; a request without it gets one.
	RequestHeader = "X-Blackbox-AI-Request"
	// CancelPathSuffix is appended to the mount prefix for the cancel
	// route: POST {prefix}/requests/{id}/cancel.
	CancelPathSuffix = "/requests/{id}/cancel"
	// DefaultMaxRunDuration bounds one request, however long the model
	// keeps calling tools.
	DefaultMaxRunDuration = 30 * time.Minute
	// CancelRetention is how long an early cancellation (one arriving
	// before its request) is remembered.
	CancelRetention = time.Minute
	// CancelWait is how long a cancel request waits for the run to end
	// before answering.
	CancelWait = 5 * time.Second
	// maxPendingCancels bounds the early-cancellation set.
	maxPendingCancels = 1024
)

// Options configures a Host.
type Options struct {
	// Instance configures the hosted instance. Name is also the natsapi
	// service name (api.{Name}.{METHOD}.{resource}), so it must be a valid
	// natsapi service name such as "inventory_service".
	Instance instance.Options
	// Bearer authenticates every conversation route. Required unless Auth
	// is set, in which case it defaults to Auth.Bearer.
	Bearer func(http.HandlerFunc) http.HandlerFunc
	// RequireAdmin wraps the administrator-only route (document ingestion).
	// Defaults to Auth.RequireAdmin(AdminAccountID) when Auth is set,
	// otherwise to Bearer.
	RequireAdmin func(http.HandlerFunc) http.HandlerFunc
	// Auth is the service's auth, the usual source of Bearer and
	// RequireAdmin. Optional when both are given.
	Auth *admin.Auth
	// AdminAccountID is the platform admin account, for RequireAdmin.
	AdminAccountID string
	// MountPrefix is where the conversation routes live on the NATS side.
	// Default ai.DefaultMountPrefix; the gateway relays to the default.
	MountPrefix string
	// MaxRunDuration bounds one request. Zero means DefaultMaxRunDuration.
	MaxRunDuration time.Duration
	// NATSAPI tunes the bridge (Version, Description, QueueGroup, chunking);
	// Service and Handler are set by the host.
	NATSAPI natsapi.Options
	// Logger receives operational lines; nil uses Instance.Logger.
	Logger *logger.Logger
}

type activeRequest struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// Host is one instance served over NATS.
type Host struct {
	inst    *instance.Instance
	api     *natsapi.Service
	router  *mux.Router
	log     *logger.Logger
	maxRun  time.Duration
	mu      sync.Mutex
	closed  bool
	active  map[string]*activeRequest
	pending map[string]time.Time // early cancellations by request key
	wg      sync.WaitGroup

	closeOnce sync.Once
	closeErr  error
}

// New opens the instance and builds the authenticated router the NATS
// bridge dispatches to. Nothing is served until Serve.
func New(o Options) (*Host, error) {
	if o.Bearer == nil && o.Auth != nil {
		o.Bearer = o.Auth.Bearer
	}
	if o.Bearer == nil {
		return nil, errors.New("ai/instance/natshost: Bearer or Auth is required")
	}
	if o.RequireAdmin == nil {
		if o.Auth != nil {
			auth, account := o.Auth, o.AdminAccountID
			o.RequireAdmin = func(h http.HandlerFunc) http.HandlerFunc { return auth.RequireAdmin(account, h) }
		} else {
			o.RequireAdmin = o.Bearer
		}
	}
	if !natsapi.ValidService(o.Instance.Name) {
		return nil, errors.New("ai/instance/natshost: Instance.Name must be a valid natsapi service name")
	}
	if o.MaxRunDuration <= 0 {
		o.MaxRunDuration = DefaultMaxRunDuration
	}
	prefix := strings.TrimRight(o.MountPrefix, "/")
	if prefix == "" {
		prefix = ai.DefaultMountPrefix
	}
	inst, err := instance.New(o.Instance)
	if err != nil {
		return nil, err
	}
	log := o.Logger
	if log == nil {
		log = inst.Options().Logger
	}
	h := &Host{inst: inst, log: log, maxRun: o.MaxRunDuration, active: map[string]*activeRequest{}, pending: map[string]time.Time{}}
	router := mux.NewRouter()
	id := admin.NewID(admin.IDConfig{Logger: log})
	router.Use(id.Middleware)
	bearer := func(f http.HandlerFunc) http.HandlerFunc { return o.Bearer(h.track(f)) }
	requireAdmin := func(f http.HandlerFunc) http.HandlerFunc { return o.RequireAdmin(h.track(f)) }
	inst.Mount(router, prefix, bearer, requireAdmin)
	router.HandleFunc(prefix+CancelPathSuffix, o.Bearer(h.cancelRequest)).Methods(http.MethodPost)
	h.router = router
	apiOpts := o.NATSAPI
	apiOpts.Service, apiOpts.Handler = o.Instance.Name, router
	if apiOpts.Logger == nil {
		apiOpts.Logger = log
	}
	h.api, err = natsapi.New(apiOpts)
	if err != nil {
		_ = inst.Close()
		return nil, err
	}
	return h, nil
}

// Instance returns the hosted instance (its store for metrics, its tools).
func (h *Host) Instance() *instance.Instance { return h.inst }

// Handler returns the authenticated router the bridge dispatches to, for a
// host that also wants to serve it over HTTP, or for tests.
func (h *Host) Handler() http.Handler { return h.router }

// Serve answers conversation requests arriving on nc, pinned to
// accountID. Serving an account twice is a no-op.
func (h *Host) Serve(accountID string, nc *nats.Conn) error { return h.api.Serve(accountID, nc) }

// Stop stops serving an account and cancels its active runs. The
// connection stays open; the gateway owns it.
func (h *Host) Stop(accountID string) {
	_ = h.api.Stop(accountID)
	h.mu.Lock()
	for key, request := range h.active {
		if strings.HasPrefix(key, accountID+":") {
			request.cancel()
		}
	}
	h.mu.Unlock()
}

// Accounts returns the IDs of the accounts currently served.
func (h *Host) Accounts() []string { return h.api.Accounts() }

func requestKey(r *http.Request, id string) string {
	return admin.AccountIDFromContext(r.Context()) + ":" + admin.UserIDFromContext(r.Context()) + ":" + id
}

// track gives a request its identity, duration bound and cancellation,
// and refuses a duplicate or an already cancelled one.
func (h *Host) track(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), h.maxRun)
		defer cancel()
		id := r.Header.Get(RequestHeader)
		if id == "" {
			id = uuid.NewString()
		} else if _, err := uuid.Parse(id); err != nil {
			http.Error(w, "invalid request ID", http.StatusBadRequest)
			return
		}
		key := requestKey(r, id)
		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			http.Error(w, "assistant is stopping", http.StatusServiceUnavailable)
			return
		}
		if until, ok := h.pending[key]; ok {
			delete(h.pending, key)
			if time.Now().Before(until) {
				h.mu.Unlock()
				http.Error(w, "request cancelled", http.StatusRequestTimeout)
				return
			}
		}
		if _, exists := h.active[key]; exists {
			h.mu.Unlock()
			http.Error(w, "request is already running", http.StatusConflict)
			return
		}
		request := &activeRequest{cancel: cancel, done: make(chan struct{})}
		h.active[key] = request
		h.wg.Add(1)
		h.mu.Unlock()
		defer func() {
			h.mu.Lock()
			delete(h.active, key)
			close(request.done)
			h.mu.Unlock()
			h.wg.Done()
		}()
		next(w, r.WithContext(ctx))
	}
}

// cancelRequest ends the run named by the path, for the caller that
// started it (the key carries account and user). A cancel that arrives
// before its request is kept for CancelRetention so the request is refused
// when it does arrive.
func (h *Host) cancelRequest(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if _, err := uuid.Parse(id); err != nil {
		http.Error(w, "invalid request ID", http.StatusBadRequest)
		return
	}
	key := requestKey(r, id)
	h.mu.Lock()
	request := h.active[key]
	if request != nil {
		request.cancel()
	} else {
		now := time.Now()
		for k, until := range h.pending {
			if now.After(until) || len(h.pending) >= maxPendingCancels {
				delete(h.pending, k)
			}
		}
		h.pending[key] = now.Add(CancelRetention)
	}
	h.mu.Unlock()
	if request != nil {
		select {
		case <-request.done:
		case <-r.Context().Done():
		case <-time.After(CancelWait):
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// Close stops serving, cancels and joins every active run, then closes
// the instance (memory, sessions, usage). It is idempotent.
func (h *Host) Close() error {
	h.closeOnce.Do(func() {
		h.mu.Lock()
		h.closed = true
		for _, request := range h.active {
			request.cancel()
		}
		h.mu.Unlock()
		h.api.Close()
		h.wg.Wait()
		h.closeErr = h.inst.Close()
	})
	return h.closeErr
}
