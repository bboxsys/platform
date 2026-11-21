package admin

import (
	"fmt"
	"io"
	"net/http"

	"github.com/bboxsys/platform/server"
)

// GetServicesStatus proxies the NATS monitoring /connz endpoint.
func (adminServer *AdminServer) GetServicesStatus(w http.ResponseWriter, r *http.Request) {
	monitoringPort := adminServer.monitoringPort
	if monitoringPort <= 0 {
		http.Error(w, `{"error":"monitoring port not configured"}`, http.StatusServiceUnavailable)
		return
	}

	url := fmt.Sprintf("http://localhost:%d/connz?subs=1", monitoringPort)
	resp, err := http.Get(url)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("services status: error proxying to NATS monitoring")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			adminServer.Logger.ErrorE().Err(err).Msg("services status: error closing response body")
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("services status: error copying response")
	}
}

// GetServerVarz proxies the NATS monitoring /varz endpoint.
func (adminServer *AdminServer) GetServerVarz(w http.ResponseWriter, r *http.Request) {
	monitoringPort := adminServer.monitoringPort
	if monitoringPort <= 0 {
		http.Error(w, `{"error":"monitoring port not configured"}`, http.StatusServiceUnavailable)
		return
	}

	url := fmt.Sprintf("http://localhost:%d/varz", monitoringPort)
	resp, err := http.Get(url)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("server varz: error proxying to NATS monitoring")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			adminServer.Logger.ErrorE().Err(err).Msg("server varz: error closing response body")
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("server varz: error copying response")
	}
}
