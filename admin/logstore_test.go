package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/bboxsys/platform/logentry"
	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/platform/server"
	"github.com/stretchr/testify/require"
)

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), v), rec.Body.String())
}

// seedLogStore opens a log store in a temp dir with n entries one second
// apart ending at end, levels alternating info and error.
func seedLogStore(t *testing.T, n int, end time.Time) *LogStore {
	t.Helper()
	ls, err := NewLogStore(t.TempDir()+"/logs.db", 10000, logger.NewLogger("test", "log_store"))
	require.NoError(t, err)
	t.Cleanup(func() { ls.Close() })
	for i := 0; i < n; i++ {
		ts := end.Add(-time.Duration(n-1-i) * time.Second)
		level := "info"
		if i%2 == 1 {
			level = "error"
		}
		require.NoError(t, ls.PutLogEntry(&logentry.LogEntry{
			ID:        ts.UTC().Format("20060102150405.000"),
			Timestamp: ts.UnixMilli(),
			Service:   "admin_server",
			Level:     level,
			Message:   "entry",
		}))
	}
	return ls
}

func TestLogStoreTimeWindow(t *testing.T) {
	end := time.Now().Truncate(time.Second)
	ls := seedLogStore(t, 10, end)

	// since alone: the last 3 seconds inclusive -> 4 entries
	entries, total, err := ls.GetLogEntries(&LogQuery{Since: end.Add(-3 * time.Second)})
	require.NoError(t, err)
	require.Equal(t, 4, total)
	require.Len(t, entries, 4)
	require.Equal(t, end.UnixMilli(), entries[0].Timestamp, "newest first by default")

	// until alone
	count, err := ls.GetLogEntriesCount(&LogQuery{Until: end.Add(-7 * time.Second)})
	require.NoError(t, err)
	require.Equal(t, 3, count)

	// both ends plus a level
	count, err = ls.GetLogEntriesCount(&LogQuery{Since: end.Add(-5 * time.Second), Until: end.Add(-1 * time.Second), Level: "error"})
	require.NoError(t, err)
	require.Equal(t, 2, count) // indices 4..8 from the start; 5 and 7 are error

	// no filters keeps the fast path
	count, err = ls.GetLogEntriesCount(&LogQuery{})
	require.NoError(t, err)
	require.Equal(t, 10, count)
}

func TestParseLogTime(t *testing.T) {
	now := time.Date(2026, 10, 18, 20, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Time
	}{
		{"", time.Time{}},
		{"2026-10-18T19:30:00Z", now.Add(-30 * time.Minute)},
		{"15m", now.Add(-15 * time.Minute)},
		{"2h", now.Add(-2 * time.Hour)},
		{strconv.FormatInt(now.Add(-2*time.Hour).Unix(), 10), now.Add(-2 * time.Hour)},
		{strconv.FormatInt(now.Add(-2*time.Hour).UnixMilli(), 10), now.Add(-2 * time.Hour)},
	}
	for _, c := range cases {
		got, err := parseLogTime(c.in, now)
		require.NoError(t, err, c.in)
		require.True(t, got.Equal(c.want), "%s: got %s want %s", c.in, got, c.want)
	}
	for _, bad := range []string{"yesterday", "-5m", "2026-13-01"} {
		_, err := parseLogTime(bad, now)
		require.Error(t, err, bad)
	}
}

func TestGetLogsDefaultLimitAndWindow(t *testing.T) {
	end := time.Now().Truncate(time.Second)
	adminServer := &AdminServer{Server: &server.Server{Logger: logger.NewLogger("test", "admin")}, logStore: seedLogStore(t, DefaultLogLimit+50, end)}

	get := func(q string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		adminServer.GetLogs(rec, httptest.NewRequest(http.MethodGet, "/api/v1/logs"+q, nil))
		return rec
	}
	var entries []logentry.LogEntry

	rec := get("")
	require.Equal(t, http.StatusOK, rec.Code)
	decodeJSON(t, rec, &entries)
	require.Len(t, entries, DefaultLogLimit, "a bare call returns one default page, not the whole store")

	since := end.Add(-5 * time.Second).UTC().Format(time.RFC3339)
	rec = get("?since=" + since + "&level=error")
	require.Equal(t, http.StatusOK, rec.Code)
	decodeJSON(t, rec, &entries)
	require.Len(t, entries, 3)
	for _, e := range entries {
		require.Equal(t, "error", e.Level)
		require.GreaterOrEqual(t, e.Timestamp, end.Add(-5*time.Second).UnixMilli())
	}

	rec = get("?since=tomorrow")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "since:")

	count := func(q string) int {
		rec := httptest.NewRecorder()
		adminServer.GetLogsCount(rec, httptest.NewRequest(http.MethodGet, "/api/v1/logs/count"+q, nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var out struct {
			Count int `json:"count"`
		}
		decodeJSON(t, rec, &out)
		return out.Count
	}
	require.Equal(t, 6, count("?since="+since))
	require.Equal(t, DefaultLogLimit+50, count("?since=1h"), "a duration is that long before now")
	require.Equal(t, 0, count("?until=1h"))
}

func TestGetLogsSummary(t *testing.T) {
	end := time.Now().Truncate(time.Second)
	adminServer := &AdminServer{Server: &server.Server{Logger: logger.NewLogger("test", "admin")}, logStore: seedLogStore(t, 10, end)}
	get := func(q string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		adminServer.GetLogsSummary(rec, httptest.NewRequest(http.MethodGet, "/api/v1/logs/summary"+q, nil))
		return rec
	}
	var out struct {
		By      string      `json:"by"`
		Buckets []LogBucket `json:"buckets"`
	}

	rec := get("?by=level")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	decodeJSON(t, rec, &out)
	require.Equal(t, "level", out.By)
	require.Equal(t, []LogBucket{{Key: "error", Count: 5}, {Key: "info", Count: 5}}, out.Buckets, "ties sort by key")

	rec = get("") // default: by message, every entry says "entry"
	require.Equal(t, http.StatusOK, rec.Code)
	decodeJSON(t, rec, &out)
	require.Equal(t, "message", out.By)
	require.Equal(t, []LogBucket{{Key: "entry", Count: 10}}, out.Buckets)

	rec = get("?by=level&limit=1&since=" + end.Add(-2*time.Second).UTC().Format(time.RFC3339))
	require.Equal(t, http.StatusOK, rec.Code)
	decodeJSON(t, rec, &out)
	require.Equal(t, []LogBucket{{Key: "error", Count: 2}}, out.Buckets, "indices 7..9: 7 and 9 are error")

	rec = get("?by=color")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "by: unknown field")
}
