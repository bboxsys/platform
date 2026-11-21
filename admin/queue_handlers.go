package admin

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
)

// queueServiceKind is the service endpoint kind the queue proxy forwards to.
const queueServiceKind = "queue_service"

// GetQueueJobs proxies the queue service's job list (GET /api/v1/queue) so
// the admin SPA reads it same-origin with its own bearer token.
func (adminServer *AdminServer) GetQueueJobs(w http.ResponseWriter, r *http.Request) {
	adminServer.proxyQueue(w, r, "/api/v1/queue")
}

// GetQueueJob proxies one job with its latest status (GET /api/v1/queue/{id}).
func (adminServer *AdminServer) GetQueueJob(w http.ResponseWriter, r *http.Request) {
	adminServer.proxyQueue(w, r, "/api/v1/queue/"+url.PathEscape(mux.Vars(r)["id"]))
}

// proxyQueue forwards a GET to the queue service named by the queue_service
// endpoint, passing the caller's bearer token through. The queue service
// validates that token itself, so this adds no authority: it only spares the
// SPA a cross-origin call to another port.
func (adminServer *AdminServer) proxyQueue(w http.ResponseWriter, r *http.Request, path string) {
	base, err := adminServer.queueServiceURL()
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("queue proxy: error reading service endpoints")
		http.Error(w, `{"error":"queue service lookup failed"}`, http.StatusInternalServerError)
		return
	}
	if base == "" {
		http.Error(w, `{"error":"queue service not configured"}`, http.StatusServiceUnavailable)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, strings.TrimRight(base, "/")+path, nil)
	if err != nil {
		http.Error(w, `{"error":"queue service request failed"}`, http.StatusInternalServerError)
		return
	}
	// The queue service runs the same ID + Bearer middleware as every
	// service, so it needs the account hint alongside the token.
	for _, h := range []string{"Authorization", "X-Blackbox-AccountID", "X-Blackbox-UserID"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	req.Header.Set("Accept", "application/json")
	resp, err := adminServer.queueHTTPClient().Do(req)
	if err != nil {
		adminServer.Logger.ErrorE().Str("url", req.URL.String()).Err(err).Msg("queue proxy: queue service unreachable")
		http.Error(w, `{"error":"queue service unavailable"}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("queue proxy: error copying response")
	}
}

// queueServiceURL returns the queue_service endpoint URL, or "" when none is
// published in the service endpoint map.
func (adminServer *AdminServer) queueServiceURL() (string, error) {
	endpoints, err := adminServer.st.GetServiceEndpoints()
	if err != nil {
		return "", err
	}
	return endpoints[queueServiceKind].URL, nil
}

var (
	queueClientOnce sync.Once
	queueClient     *http.Client
)

// queueHTTPClient is the client the proxy uses: it trusts the system roots
// plus the CA the admin server was given, which in a self-signed profile is
// the queue server's own certificate.
func (adminServer *AdminServer) queueHTTPClient() *http.Client {
	queueClientOnce.Do(func() {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if adminServer.ServerOptions != nil && len(adminServer.ServerOptions.CA) > 0 {
			pool.AppendCertsFromPEM(adminServer.ServerOptions.CA)
		}
		queueClient = &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
			},
		}
	})
	return queueClient
}
