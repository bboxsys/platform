package queue

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/bboxsys/platform/job"
	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/platform/messenger"
	"github.com/bboxsys/platform/server"
	"github.com/bboxsys/platform/worker"
	"github.com/gorilla/mux"
	natsd "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

func newTestServer(t *testing.T) *QueueServer {
	t.Helper()
	srv, err := natsd.NewServer(&natsd.Options{Host: "127.0.0.1", Port: -1, NoSigs: true, NoLog: true})
	if err != nil {
		t.Fatalf("nats server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats server did not start")
	}
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	mc, err := messenger.NewClientFromConn(nc, "platform", "queue_test")
	if err != nil {
		t.Fatalf("messenger client: %v", err)
	}
	t.Cleanup(mc.Close)
	st, err := NewStore(&StoreOptions{Location: filepath.Join(t.TempDir(), "queue.db"), Application: "platform", Component: "queue_test"})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	l := logger.NewLogger("platform", "queue_test")
	qs := &QueueServer{Server: &server.Server{MessengerClient: mc, Logger: l}, st: st}
	qs.queue = &Queue{Store: st, MessengerClient: mc, Logger: l}
	if err := qs.queue.Start(); err != nil {
		t.Fatalf("relay: %v", err)
	}
	t.Cleanup(qs.queue.Stop)
	return qs
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRelayStoresAndForwards(t *testing.T) {
	qs := newTestServer(t)
	mc := qs.MessengerClient

	w := worker.New(&worker.Options{Service: "media", JobName: "thumbnail", MC: mc})
	if _, err := w.Run(func(j *job.Job) (string, error) { return "done", nil }); err != nil {
		t.Fatal(err)
	}

	j := job.New("media", "thumbnail", map[string]string{"item": "1"})
	if err := j.Dispatch(mc); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "complete status in store", func() bool {
		st, _ := qs.st.GetJobStatus(j.ID)
		return st != nil && st.State == job.StateComplete
	})
	rec, err := qs.st.GetRecord(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Job.Service != "media" || rec.Job.Name != "thumbnail" || rec.Status.Result != "done" {
		t.Errorf("record = %+v / %+v", rec.Job, rec.Status)
	}
	if _, err := qs.st.GetRecord("missing"); err != ErrNotFound {
		t.Errorf("GetRecord(missing) err = %v, want ErrNotFound", err)
	}
}

func TestHTTPSubmitListAndGet(t *testing.T) {
	qs := newTestServer(t)
	router := mux.NewRouter()
	router.HandleFunc("/api/v1/queue", qs.ListJobs).Methods("GET")
	router.HandleFunc("/api/v1/queue", qs.Queue).Methods("POST")
	router.HandleFunc("/api/v1/queue/{id}", qs.GetJob).Methods("GET")

	for _, body := range []string{`{"name":"thumbnail"}`, `not json`} {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest("POST", "/api/v1/queue", bytes.NewBufferString(body)))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("POST %s: code = %d, want 400", body, rr.Code)
		}
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest("POST", "/api/v1/queue",
		bytes.NewBufferString(`{"service":"media","name":"thumbnail","payload":{"item":"7"}}`)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST code = %d, body %s", rr.Code, rr.Body.String())
	}
	var created map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil || created["id"] == "" {
		t.Fatalf("POST body = %s (err %v)", rr.Body.String(), err)
	}
	id := created["id"]

	waitFor(t, "job in store", func() bool { _, err := qs.st.GetJob(id); return err == nil })

	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/queue/"+id, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET one code = %d", rr.Code)
	}
	var rec Record
	if err := json.Unmarshal(rr.Body.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Job.ID != id || rec.Job.Service != "media" || rec.Status != nil {
		t.Errorf("record = %+v / %+v", rec.Job, rec.Status)
	}

	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/queue/nope", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("GET missing code = %d, want 404", rr.Code)
	}

	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/queue", nil))
	var list []Record
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Job.ID != id {
		t.Errorf("list = %+v", list)
	}
}
