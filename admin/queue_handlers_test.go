package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

func TestQueueProxy(t *testing.T) {
	adminServer := newTestAdminServer(t, t.TempDir()+"/admin.db")
	router := mux.NewRouter()
	router.HandleFunc("/api/v1/queue", adminServer.GetQueueJobs).Methods("GET")
	router.HandleFunc("/api/v1/queue/{id}", adminServer.GetQueueJob).Methods("GET")

	// No queue_service endpoint published yet.
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/queue", nil))
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)

	var gotPath, gotAuth, gotAccount string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccount = r.Header.Get("X-Blackbox-AccountID")
		if r.URL.Path == "/api/v1/queue/missing" {
			http.Error(w, "job not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"job":{"id":"1","service":"media","name":"thumbnail"}}]`))
	}))
	defer upstream.Close()
	require.NoError(t, adminServer.st.PutServiceEndpoint(ServiceEndpoint{Kind: queueServiceKind, URL: upstream.URL + "/"}))

	req := httptest.NewRequest("GET", "/api/v1/queue", nil)
	req.Header.Set("Authorization", "Bearer token-123")
	req.Header.Set("X-Blackbox-AccountID", "ACC123")
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "/api/v1/queue", gotPath)
	require.Equal(t, "Bearer token-123", gotAuth)
	require.Equal(t, "ACC123", gotAccount)
	require.Contains(t, rr.Body.String(), `"service":"media"`)

	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/queue/missing", nil))
	require.Equal(t, http.StatusNotFound, rr.Code)
	require.Equal(t, "/api/v1/queue/missing", gotPath)

	upstream.Close()
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/queue", nil))
	require.Equal(t, http.StatusBadGateway, rr.Code)
}
