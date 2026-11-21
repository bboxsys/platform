package queue

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"

	admin "github.com/bboxsys/platform/admin/client"
	"github.com/bboxsys/platform/job"
	"github.com/bboxsys/platform/server"
	"github.com/bboxsys/store"
	"github.com/gorilla/mux"
)

// QueueServer is the queue relay as an HTTPS service. Build it with
// NewQueueServer from the usual server.ServerOptions (Messenger must be
// true) plus Metadata["storeLocation"], then Start it in a routine.
//
// Routes (all bearer-authenticated):
//
//	GET  /api/v1/noop        session check
//	GET  /api/v1/queue       every stored job with its latest status
//	POST /api/v1/queue       submit a job {service, name, payload}; returns its id
//	GET  /api/v1/queue/{id}  one job with its latest status
type QueueServer struct {
	*server.Server
	st    *Store
	queue *Queue
	auth  *admin.Auth
	mu    sync.Mutex
	ready bool
}

// NewQueueServer builds the server and opens its store.
func NewQueueServer(o *server.ServerOptions) (*QueueServer, error) {
	s, err := server.NewServer(o)
	if err != nil {
		return nil, err
	}
	location, _ := o.GetMetadata("storeLocation").(string)
	if location == "" {
		return nil, errors.New("queue: Metadata[\"storeLocation\"] is required")
	}
	st, err := NewStore(&StoreOptions{
		Location:    location,
		Application: o.Application,
		Component:   "queue_store",
	})
	if err != nil {
		return nil, err
	}
	return &QueueServer{Server: s, st: st, auth: &admin.Auth{}}, nil
}

// Ready reports whether handlers are installed and the relay is running.
func (qs *QueueServer) Ready() bool {
	qs.mu.Lock()
	defer qs.mu.Unlock()
	return qs.ready
}

// Close stops the HTTP server, the relay, and the store.
func (qs *QueueServer) Close() {
	if err := qs.Srv.Close(); err != nil {
		qs.Logger.ErrorE().Err(err).Msg("queue: error closing server")
	}
	if qs.queue != nil {
		qs.queue.Stop()
	}
	if err := qs.st.Close(); err != nil {
		qs.Logger.ErrorE().Err(err).Msg("queue: error closing store")
	}
}

// Start installs handlers, starts the relay, and serves until closed.
func (qs *QueueServer) Start() {
	qs.AddHandlers()
	qs.SetReady()
	qs.Serve()
}

// SetReady marks the server ready.
func (qs *QueueServer) SetReady() {
	qs.mu.Lock()
	qs.ready = true
	qs.mu.Unlock()
}

// AddHandlers installs middleware and routes and starts the relay.
func (qs *QueueServer) AddHandlers() {
	id := admin.NewID(admin.IDConfig{
		Logger:    qs.Logger,
		BasicAuth: false,
	})
	qs.Router.Use(id.Middleware)
	qs.auth = admin.NewAuth(admin.AuthConfig{
		Logger:          qs.Logger,
		BasicAuth:       false,
		TrustedAccounts: qs.TrustedAccounts,
	})

	qs.Router.HandleFunc("/", qs.auth.Bearer(server.EndpointNotImplemented)).Methods("GET")
	qs.Router.HandleFunc("/api/v1/noop", qs.auth.Bearer(qs.Noop)).Methods("GET")
	qs.Router.HandleFunc("/api/v1/queue", qs.auth.Bearer(qs.ListJobs)).Methods("GET")
	qs.Router.HandleFunc("/api/v1/queue", qs.auth.Bearer(qs.Queue)).Methods("POST")
	qs.Router.HandleFunc("/api/v1/queue/{id}", qs.auth.Bearer(qs.GetJob)).Methods("GET")

	qs.queue = &Queue{Store: qs.st, MessengerClient: qs.MessengerClient, Logger: qs.Logger}
	if err := qs.queue.Start(); err != nil {
		qs.Logger.ErrorE().Err(err).Msg("queue: relay did not start; jobs will not be stored or forwarded")
	} else {
		qs.Logger.Info("queue: relay started")
	}
	qs.Logger.Info("queue: server started")
}

// Queue accepts a job {service, name, payload}, dispatches it, and answers
// 201 with {"status": "created", "id": ...}.
func (qs *QueueServer) Queue(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		qs.Logger.ErrorE().Err(err).Msg("queue: error reading body")
		return
	}
	j := job.Job{}
	if err := json.Unmarshal(body, &j); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		qs.Logger.ErrorE().Err(err).Msg("queue: error unmarshaling job from body")
		return
	}
	if j.Service == "" || j.Name == "" {
		http.Error(w, "service and name are required", http.StatusBadRequest)
		return
	}
	if j.ID == "" {
		j.ID = job.NewID()
	}
	if err := j.Dispatch(qs.MessengerClient); err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		qs.Logger.ErrorE().Str("id", j.ID).Err(err).Msg("queue: error dispatching job")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "created", "id": j.ID})
}

// ListJobs answers every stored job with its latest status.
func (qs *QueueServer) ListJobs(w http.ResponseWriter, r *http.Request) {
	records, err := qs.st.ListRecords()
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		qs.Logger.ErrorE().Err(err).Msg("queue: error listing jobs")
		return
	}
	writeJSON(w, http.StatusOK, records)
}

// GetJob answers one stored job with its latest status.
func (qs *QueueServer) GetJob(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	record, err := qs.st.GetRecord(id)
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		qs.Logger.ErrorE().Str("id", id).Err(err).Msg("queue: error reading job")
		return
	}
	writeJSON(w, http.StatusOK, record)
}

// Noop answers 200 for authentication checks and session extension.
func (qs *QueueServer) Noop(w http.ResponseWriter, r *http.Request) {}

// GetBaseStore returns the underlying store for metrics collection.
func (qs *QueueServer) GetBaseStore() *store.Store {
	return qs.st.Store
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
