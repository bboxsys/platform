package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bboxsys/platform/ai/instance/natshost"
	"github.com/bboxsys/platform/natsapi"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/nats-io/nats.go"
)

const (
	// AssistantServicesPath lists the conversation endpoints the console
	// can select; AssistantServicesPath/{service}/... relays to one.
	AssistantServicesPath = AIMountPrefix + "/services"
	// AdminAssistantID is the id of the admin's own assistant in the list;
	// the console reaches it at AIMountPrefix directly.
	AdminAssistantID = "admin"
	// DefaultAssistantRelayTimeout bounds the wait for each message of a
	// relayed response.
	DefaultAssistantRelayTimeout = 2 * time.Minute
	// assistantCancelWait bounds the cancel sent for an abandoned relay.
	assistantCancelWait = 5 * time.Second
)

var assistantIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// AssistantService is one conversation endpoint a product hosts (normally
// a natshost.Host) and registers with the gateway so the console can
// select it. The registry holds explicitly registered endpoints only: a
// usage heartbeat does not make an instance selectable.
type AssistantService struct {
	// ID is the stable identifier in the console and the relay URL:
	// lower-case letters, digits, "_" and "-"; not "admin".
	ID string
	// Name is the display name.
	Name string
	// Service is the natsapi service the conversation API is served as.
	// Default ID + "_service".
	Service string
	// Serve binds the endpoint to a connection inside accountID the first
	// time the gateway relays for that account (natshost.Host.Serve). A
	// remote service with its own subscription lifecycle leaves it nil.
	Serve func(accountID string, nc *nats.Conn) error
	// Stop releases what Serve bound, when the account is removed, the
	// service is unregistered or the gateway closes (natshost.Host.Stop).
	Stop func(accountID string)
}

// AssistantServiceInfo is one entry of GET /api/v1/ai/services.
type AssistantServiceInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AssistantGatewayOptions configures MountAssistantGateway.
type AssistantGatewayOptions struct {
	// Connect returns a NATS connection inside accountID and a release
	// function the gateway calls when it drops the account. Every relayed
	// conversation travels on the caller's own account, never a shared
	// service account. Nil uses the collectors' connection for the account
	// (StartCollectors), which the admin already holds for every account;
	// a host with its own controller wiring supplies its own.
	Connect func(accountID string) (nc *nats.Conn, release func(), err error)
	// Timeout bounds the wait for each message of a relayed response. Zero
	// means DefaultAssistantRelayTimeout.
	Timeout time.Duration
}

type assistantConn struct {
	nc      *nats.Conn
	release func()
	served  map[string]bool // service IDs Serve was called for
}

// AssistantGateway is the registry of service assistants and the relay
// that carries console conversations to them over NATS.
type AssistantGateway struct {
	adminServer *AdminServer
	opts        AssistantGatewayOptions
	mu          sync.Mutex
	closed      bool
	services    map[string]*AssistantService
	conns       map[string]*assistantConn
	// all is the coordinating assistant (assistant_all.go), created once
	// the admin's assistant has started and a service is registered.
	all *allAssistant
}

// MountAssistantGateway registers GET /api/v1/ai/services and the relay
// /api/v1/ai/services/{service}/{path} on the admin router, behind the
// bearer auth the server builds in AddHandlers (resolved per request, so
// it may be called before or after). The relay accepts only the
// conversation routes (status and sessions), forwards the caller's bearer
// and account, names each request so an abandoned relay cancels the
// remote run, and streams the answer as it arrives. A product registers
// its services with Register; the admin's own assistant is listed first.
// It is idempotent.
func (adminServer *AdminServer) MountAssistantGateway(o AssistantGatewayOptions) *AssistantGateway {
	adminServer.mu.Lock()
	defer adminServer.mu.Unlock()
	if adminServer.assistants != nil {
		return adminServer.assistants
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultAssistantRelayTimeout
	}
	g := &AssistantGateway{adminServer: adminServer, opts: o, services: map[string]*AssistantService{}, conns: map[string]*assistantConn{}}
	if g.opts.Connect == nil {
		g.opts.Connect = g.collectorConnection
	}
	bearer := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { adminServer.auth.Bearer(h)(w, r) }
	}
	adminServer.Router.HandleFunc(AssistantServicesPath, bearer(g.list)).Methods(http.MethodGet)
	adminServer.Router.HandleFunc(AssistantServicesPath+"/{service}/{path:.*}", bearer(g.relay))
	adminServer.assistants = g
	return g
}

// AssistantGateway returns the mounted gateway, or nil before
// MountAssistantGateway.
func (adminServer *AdminServer) AssistantGateway() *AssistantGateway {
	adminServer.mu.Lock()
	defer adminServer.mu.Unlock()
	return adminServer.assistants
}

func (adminServer *AdminServer) closeAssistantGateway() {
	if g := adminServer.AssistantGateway(); g != nil {
		g.Close()
	}
}

// collectorConnection is the default Connect: the collectors' connection
// inside the account.
func (g *AssistantGateway) collectorConnection(accountID string) (*nats.Conn, func(), error) {
	col := g.adminServer.Collectors()
	if col == nil {
		return nil, nil, errors.New("no collectors: supply AssistantGatewayOptions.Connect")
	}
	nc := col.Conn(accountID)
	if nc == nil {
		return nil, nil, fmt.Errorf("no collector connection for account %s", accountID)
	}
	return nc, func() {}, nil
}

// Register adds a service to the registry. Registering an ID twice
// replaces the entry. The first registration also starts the All
// assistant when the admin's own assistant is running (StartAI starts it
// otherwise, once services exist).
func (g *AssistantGateway) Register(s AssistantService) error {
	if !assistantIDPattern.MatchString(s.ID) || s.ID == AdminAssistantID || s.ID == AllAssistantID {
		return fmt.Errorf("assistant gateway: invalid service id %q", s.ID)
	}
	if s.Name == "" {
		s.Name = strings.ToUpper(s.ID[:1]) + s.ID[1:]
	}
	if s.Service == "" {
		s.Service = s.ID + "_service"
	}
	if !natsapi.ValidService(s.Service) {
		return fmt.Errorf("assistant gateway: invalid natsapi service %q", s.Service)
	}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return errors.New("assistant gateway: closed")
	}
	if old := g.services[s.ID]; old != nil {
		g.stopLocked(old)
	}
	service := s
	g.services[s.ID] = &service
	g.mu.Unlock()
	// Outside the lock: ensureAll takes it, and aiStarted takes the admin
	// server's AI lock, which StartAI holds while it calls ensureAll.
	inst, opts := g.adminServer.aiStarted()
	if err := g.ensureAll(inst, opts); err != nil {
		g.adminServer.Logger.ErrorE().Err(err).Msg("assistant gateway: starting the All assistant")
	}
	return nil
}

// Unregister removes a service and stops it on every account it was
// bound to. Unknown IDs are ignored.
func (g *AssistantGateway) Unregister(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if s := g.services[id]; s != nil {
		g.stopLocked(s)
		delete(g.services, id)
	}
}

// stopLocked calls Stop for every account the service was served on.
func (g *AssistantGateway) stopLocked(s *AssistantService) {
	for accountID, conn := range g.conns {
		if conn.served[s.ID] {
			delete(conn.served, s.ID)
			if s.Stop != nil {
				s.Stop(accountID)
			}
		}
	}
}

// Services lists the selectable assistants: the admin's own first, then
// the registered services by name, then All (the coordinator that asks
// the others) when it is running.
func (g *AssistantGateway) Services() []AssistantServiceInfo {
	out := []AssistantServiceInfo{{ID: AdminAssistantID, Name: AdminAssistantName}}
	g.mu.Lock()
	registered := make([]AssistantServiceInfo, 0, len(g.services))
	for _, s := range g.services {
		registered = append(registered, AssistantServiceInfo{ID: s.ID, Name: s.Name})
	}
	all := g.all != nil && len(g.services) > 0
	g.mu.Unlock()
	sort.Slice(registered, func(i, j int) bool { return registered[i].Name < registered[j].Name })
	out = append(out, registered...)
	if all {
		out = append(out, AssistantServiceInfo{ID: AllAssistantID, Name: AllAssistantName})
	}
	return out
}

// RemoveAccount stops every service on the account and releases its
// connection; the admin calls it when an account is deleted.
func (g *AssistantGateway) RemoveAccount(accountID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.dropAccountLocked(accountID)
}

func (g *AssistantGateway) dropAccountLocked(accountID string) {
	conn := g.conns[accountID]
	if conn == nil {
		return
	}
	for id := range conn.served {
		if s := g.services[id]; s != nil && s.Stop != nil {
			s.Stop(accountID)
		}
	}
	delete(g.conns, accountID)
	if conn.release != nil {
		conn.release()
	}
}

// Close stops every service on every account, releases the connections
// and closes the All assistant. The services' own hosts are closed by
// their owners.
func (g *AssistantGateway) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	for accountID := range g.conns {
		g.dropAccountLocked(accountID)
	}
	g.services = map[string]*AssistantService{}
	if g.all != nil {
		if err := g.all.inst.Close(); err != nil {
			g.adminServer.Logger.ErrorE().Err(err).Msg("assistant gateway: closing the All assistant")
		}
		g.all = nil
	}
}

func (g *AssistantGateway) list(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(g.Services())
}

// client returns an HTTP client over the caller's account connection with
// the service bound to it.
func (g *AssistantGateway) client(accountID, id string) (*http.Client, *AssistantService, error) {
	account, err := g.adminServer.Store().GetAccountByID(accountID)
	if err != nil || account.ID != accountID {
		return nil, nil, errors.New("account unavailable")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil, nil, errors.New("gateway closed")
	}
	s := g.services[id]
	if s == nil {
		return nil, nil, fmt.Errorf("no assistant service %q", id)
	}
	conn := g.conns[accountID]
	if conn == nil {
		nc, release, err := g.opts.Connect(accountID)
		if err != nil {
			return nil, nil, err
		}
		if nc == nil {
			return nil, nil, errors.New("no connection for account")
		}
		conn = &assistantConn{nc: nc, release: release, served: map[string]bool{}}
		g.conns[accountID] = conn
	}
	if s.Serve != nil && !conn.served[s.ID] {
		if err := s.Serve(accountID, conn.nc); err != nil {
			return nil, nil, err
		}
		conn.served[s.ID] = true
	}
	return &http.Client{Transport: &natsapi.Transport{Conn: conn.nc, Timeout: g.opts.Timeout}}, s, nil
}

// relay forwards one conversation request to the service, or serves the
// All assistant in-process.
func (g *AssistantGateway) relay(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, path := vars["service"], vars["path"]
	// Only the conversation API: never documents, credentials or MCP.
	if path != "status" && path != "sessions" && !strings.HasPrefix(path, "sessions/") {
		http.NotFound(w, r)
		return
	}
	if strings.ContainsAny(path, "\\?#") || strings.Contains(path, "..") || strings.Contains(path, "//") {
		http.Error(w, "invalid assistant path", http.StatusBadRequest)
		return
	}
	if id == AllAssistantID {
		// The coordinator lives in this process; nothing crosses NATS until
		// one of its tools asks a service.
		g.serveAll(w, r, path)
		return
	}
	accountID := AccountIDFromContext(r.Context())
	client, s, err := g.client(accountID, id)
	if err != nil {
		g.adminServer.Logger.ErrorE().Err(err).Str("service", id).Msg("assistant gateway: connection")
		http.Error(w, "service assistant unavailable", http.StatusServiceUnavailable)
		return
	}
	requestID := uuid.NewString()
	target := AIMountPrefix + "/" + path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := natsapi.NewRequest(r.Context(), r.Method, s.Service, target, r.Body)
	if err != nil {
		http.Error(w, "invalid assistant request", http.StatusBadRequest)
		return
	}
	req.Header.Set("Authorization", r.Header.Get("Authorization"))
	req.Header.Set("X-Blackbox-AccountID", accountID)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", r.Header.Get("Accept"))
	req.Header.Set(natshost.RequestHeader, requestID)
	completed := false
	defer func() {
		if completed {
			return
		}
		// The browser went away or the copy failed: the remote run must
		// not keep spending on its own.
		ctx, cancel := context.WithTimeout(context.Background(), assistantCancelWait)
		defer cancel()
		stop, _ := natsapi.NewRequest(ctx, http.MethodPost, s.Service, AIMountPrefix+"/requests/"+url.PathEscape(requestID)+"/cancel", nil)
		stop.Header = req.Header.Clone()
		if resp, err := client.Do(stop); err == nil {
			_ = resp.Body.Close()
		}
	}()
	resp, err := client.Do(req)
	if err != nil {
		if r.Context().Err() == nil {
			http.Error(w, "assistant request failed", http.StatusBadGateway)
		}
		return
	}
	defer resp.Body.Close()
	for _, header := range []string{"Content-Type", "Cache-Control"} {
		if value := resp.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(flushingWriter{w}, resp.Body)
	completed = err == nil && r.Context().Err() == nil
}

// flushingWriter flushes after every write so streamed events reach the
// browser as they arrive.
type flushingWriter struct{ http.ResponseWriter }

func (w flushingWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
	return n, err
}
