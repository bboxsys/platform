package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bboxsys/platform/server"
	"github.com/rs/zerolog"
)

// DefaultLogLimit is the page size of GET /api/v1/logs when the request
// names none. The Logs page always sends its own; the default keeps a
// bare call (the assistant's tool, a curl) from returning the whole store.
const DefaultLogLimit = 200

// parseLogTime reads a since or until value: an RFC 3339 timestamp, Unix
// seconds or milliseconds, or a duration such as "15m" meaning that long
// before now (the assistant has no clock of its own, so "the last 15
// minutes" is the natural ask). Empty means unbounded.
func parseLogTime(value string, now time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t, nil
	}
	if n, err := strconv.ParseInt(value, 10, 64); err == nil {
		if n > 1e11 { // milliseconds: seconds would put this past year 5000
			return time.UnixMilli(n), nil
		}
		return time.Unix(n, 0), nil
	}
	if d, err := time.ParseDuration(value); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("invalid time %q: use RFC 3339, Unix seconds or milliseconds, or a duration such as 15m", value)
}

// logTimeWindow reads since and until from the query into q. It returns
// false after writing a 400 for a value it cannot parse.
func logTimeWindow(w http.ResponseWriter, r *http.Request, q *LogQuery) bool {
	now := time.Now()
	var err error
	if q.Since, err = parseLogTime(r.URL.Query().Get("since"), now); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"since: %s"}`, err), http.StatusBadRequest)
		return false
	}
	if q.Until, err = parseLogTime(r.URL.Query().Get("until"), now); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"until: %s"}`, err), http.StatusBadRequest)
		return false
	}
	return true
}

// GetLogs returns paginated log entries.
// Query params: limit (default DefaultLogLimit), offset, service, host,
// level, search, since, until, sort (asc/desc)
func (adminServer *AdminServer) GetLogs(w http.ResponseWriter, r *http.Request) {
	if adminServer.logStore == nil {
		http.Error(w, `{"error":"log store not initialized"}`, http.StatusServiceUnavailable)
		return
	}

	query := &LogQuery{
		Limit:   DefaultLogLimit,
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
	if host := r.URL.Query().Get("host"); host != "" {
		query.Host = host
	}
	if level := r.URL.Query().Get("level"); level != "" {
		query.Level = level
	}
	if search := r.URL.Query().Get("search"); search != "" {
		query.Search = search
	}
	if sort := r.URL.Query().Get("sort"); sort == "asc" || sort == "desc" {
		query.SortDir = sort
	}
	if !logTimeWindow(w, r, query) {
		return
	}

	entries, _, err := adminServer.logStore.GetLogEntries(query)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("get logs: error")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

// GetLogsCount returns the total count of log entries matching optional filters.
// Query params: service, host, level, search, since, until
func (adminServer *AdminServer) GetLogsCount(w http.ResponseWriter, r *http.Request) {
	if adminServer.logStore == nil {
		http.Error(w, `{"error":"log store not initialized"}`, http.StatusServiceUnavailable)
		return
	}

	query := &LogQuery{}
	if service := r.URL.Query().Get("service"); service != "" {
		query.Service = service
	}
	if host := r.URL.Query().Get("host"); host != "" {
		query.Host = host
	}
	if level := r.URL.Query().Get("level"); level != "" {
		query.Level = level
	}
	if search := r.URL.Query().Get("search"); search != "" {
		query.Search = search
	}
	if !logTimeWindow(w, r, query) {
		return
	}

	count, err := adminServer.logStore.GetLogEntriesCount(query)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("get logs count: error")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		Count int `json:"count"`
	}{Count: count})
}

// DefaultLogSummaryLimit is how many groups GET /api/v1/logs/summary
// returns when the request names no limit.
const DefaultLogSummaryLimit = 10

// GetLogsSummary returns the matching entries counted by one field.
// Query params: by (service, host, level, component, message; default
// message), limit (default DefaultLogSummaryLimit), service, host, level,
// search, since, until
func (adminServer *AdminServer) GetLogsSummary(w http.ResponseWriter, r *http.Request) {
	if adminServer.logStore == nil {
		http.Error(w, `{"error":"log store not initialized"}`, http.StatusServiceUnavailable)
		return
	}
	query := &LogQuery{
		Service: r.URL.Query().Get("service"),
		Host:    r.URL.Query().Get("host"),
		Level:   r.URL.Query().Get("level"),
		Search:  r.URL.Query().Get("search"),
	}
	if !logTimeWindow(w, r, query) {
		return
	}
	by := r.URL.Query().Get("by")
	if by == "" {
		by = "message"
	}
	limit := DefaultLogSummaryLimit
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}
	buckets, err := adminServer.logStore.GetLogSummary(query, by, limit)
	if err != nil {
		if strings.HasPrefix(err.Error(), "unknown field") {
			http.Error(w, fmt.Sprintf(`{"error":"by: %s"}`, err), http.StatusBadRequest)
			return
		}
		adminServer.Logger.ErrorE().Err(err).Msg("get logs summary: error")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		By      string      `json:"by"`
		Buckets []LogBucket `json:"buckets"`
	}{By: by, Buckets: buckets})
}

// GetLogLevel returns the current runtime log level.
func (adminServer *AdminServer) GetLogLevel(w http.ResponseWriter, r *http.Request) {
	level := zerolog.GlobalLevel().String()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		Level string `json:"level"`
	}{Level: level})
}

// GetLogMaxEntries returns the current log store max entries limit.
func (adminServer *AdminServer) GetLogMaxEntries(w http.ResponseWriter, r *http.Request) {
	if adminServer.logStore == nil {
		http.Error(w, `{"error":"log store not initialized"}`, http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		MaxEntries int `json:"max_entries"`
	}{MaxEntries: adminServer.logStore.MaxEntries()})
}

// SetLogMaxEntries changes the log store max entries limit at runtime.
// Expects JSON body: {"max_entries": N} where N > 0.
func (adminServer *AdminServer) SetLogMaxEntries(w http.ResponseWriter, r *http.Request) {
	if adminServer.logStore == nil {
		http.Error(w, `{"error":"log store not initialized"}`, http.StatusServiceUnavailable)
		return
	}

	var req struct {
		MaxEntries int `json:"max_entries"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4*1024)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	if req.MaxEntries <= 0 {
		http.Error(w, `{"error":"max_entries must be greater than 0"}`, http.StatusBadRequest)
		return
	}

	adminServer.logStore.SetMaxEntries(req.MaxEntries)
	adminServer.Logger.InfoE().Int("maxEntries", req.MaxEntries).Msg("log max entries changed")
	adminServer.persistConfig("logging.max_entries", req.MaxEntries)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		MaxEntries int `json:"max_entries"`
	}{MaxEntries: req.MaxEntries})
}

// SetLogLevel changes the runtime log level.
// Expects JSON body: {"level": "debug"|"info"|"warn"|"error"}
func (adminServer *AdminServer) SetLogLevel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Level string `json:"level"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4*1024)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	level, err := zerolog.ParseLevel(req.Level)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"invalid level: %s"}`, req.Level), http.StatusBadRequest)
		return
	}

	zerolog.SetGlobalLevel(level)
	adminServer.Logger.InfoE().Str("level", level.String()).Msg("log level changed")
	adminServer.persistConfig("logging.level", level.String())

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		Level string `json:"level"`
	}{Level: level.String()})
}
