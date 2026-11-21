package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/bboxsys/platform/server"
)

// GetStoreMetrics returns the latest metrics snapshot per service/store.
// GET /api/v1/metrics/stores
func (adminServer *AdminServer) GetStoreMetrics(w http.ResponseWriter, r *http.Request) {
	if adminServer.metricsStore == nil {
		http.Error(w, `{"error":"metrics store not initialized"}`, http.StatusServiceUnavailable)
		return
	}

	snapshots, err := adminServer.metricsStore.GetLatestSnapshots()
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("get store metrics: error")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snapshots)
}

// GetStoreMetricsHistory returns historical metrics entries with pagination and filtering.
// GET /api/v1/metrics/stores/history
// Query params: limit, offset, service, store_name, sort (asc/desc)
func (adminServer *AdminServer) GetStoreMetricsHistory(w http.ResponseWriter, r *http.Request) {
	if adminServer.metricsStore == nil {
		http.Error(w, `{"error":"metrics store not initialized"}`, http.StatusServiceUnavailable)
		return
	}

	query := &MetricsQuery{
		SortDir: "desc", // default newest first
	}

	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			query.Limit = l
		}
	}
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			query.Offset = o
		}
	}
	if service := r.URL.Query().Get("service"); service != "" {
		query.Service = service
	}
	if storeName := r.URL.Query().Get("store_name"); storeName != "" {
		query.StoreName = storeName
	}
	if sort := r.URL.Query().Get("sort"); sort == "asc" || sort == "desc" {
		query.SortDir = sort
	}

	entries, _, err := adminServer.metricsStore.GetMetricsEntries(query)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("get store metrics history: error")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

// GetStoreMetricsHistoryCount returns the count of historical metrics entries matching filters.
// GET /api/v1/metrics/stores/history/count
// Query params: service, store_name
func (adminServer *AdminServer) GetStoreMetricsHistoryCount(w http.ResponseWriter, r *http.Request) {
	if adminServer.metricsStore == nil {
		http.Error(w, `{"error":"metrics store not initialized"}`, http.StatusServiceUnavailable)
		return
	}

	query := &MetricsQuery{}
	if service := r.URL.Query().Get("service"); service != "" {
		query.Service = service
	}
	if storeName := r.URL.Query().Get("store_name"); storeName != "" {
		query.StoreName = storeName
	}

	count, err := adminServer.metricsStore.GetMetricsEntriesCount(query)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("get store metrics history count: error")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		Count int `json:"count"`
	}{Count: count})
}
