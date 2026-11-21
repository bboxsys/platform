package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/bboxsys/platform/ai/usage"
	"github.com/bboxsys/platform/server"
)

const aiUsageNotInitialized = `{"error":"ai usage store not initialized"}`

// aiUsageRunsResponse is the body of GET /api/v1/ai/usage/runs.
type aiUsageRunsResponse struct {
	Records []usage.Record `json:"records"`
	Total   int            `json:"total"`
}

func writeAIUsageJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// GetAIUsageInstances lists every AI instance that has reported usage.
// GET /api/v1/ai/usage/instances
func (adminServer *AdminServer) GetAIUsageInstances(w http.ResponseWriter, r *http.Request) {
	us := adminServer.aiUsageStore
	if us == nil {
		http.Error(w, aiUsageNotInitialized, http.StatusServiceUnavailable)
		return
	}
	instances, err := us.Instances()
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("get ai usage instances: error")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}
	writeAIUsageJSON(w, instances)
}

// GetAIUsageRuns lists collected run records, newest first.
// GET /api/v1/ai/usage/runs
// Query params: instance, host, account_id, model, limit (default 100, max
// 500), offset
func (adminServer *AdminServer) GetAIUsageRuns(w http.ResponseWriter, r *http.Request) {
	us := adminServer.aiUsageStore
	if us == nil {
		http.Error(w, aiUsageNotInitialized, http.StatusServiceUnavailable)
		return
	}
	v := r.URL.Query()
	q := AIRunQuery{
		Instance:  v.Get("instance"),
		Host:      v.Get("host"),
		AccountID: v.Get("account_id"),
		Model:     v.Get("model"),
	}
	if n, err := strconv.Atoi(v.Get("limit")); err == nil && n > 0 {
		q.Limit = n
	}
	if n, err := strconv.Atoi(v.Get("offset")); err == nil && n >= 0 {
		q.Offset = n
	}
	records, total, err := us.Runs(q)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("get ai usage runs: error")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}
	writeAIUsageJSON(w, aiUsageRunsResponse{Records: records, Total: total})
}

// GetAIUsageSummary totals the records since a time by a grouping.
// GET /api/v1/ai/usage/summary
// Query params: since (RFC 3339, default 30 days ago), group (instance,
// model, account, day; default instance)
func (adminServer *AdminServer) GetAIUsageSummary(w http.ResponseWriter, r *http.Request) {
	us := adminServer.aiUsageStore
	if us == nil {
		http.Error(w, aiUsageNotInitialized, http.StatusServiceUnavailable)
		return
	}
	v := r.URL.Query()
	since := time.Now().Add(-30 * 24 * time.Hour)
	if s := v.Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			http.Error(w, `{"error":"since must be an RFC 3339 time"}`, http.StatusBadRequest)
			return
		}
		since = t
	}
	group := v.Get("group")
	if group == "" {
		group = AIUsageGroupInstance
	}
	if _, err := aiUsageGroupKey(group); err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	groups, err := us.Summary(since, group)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("get ai usage summary: error")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}
	writeAIUsageJSON(w, groups)
}
