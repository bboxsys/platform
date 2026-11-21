package logger

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/bboxsys/platform/logentry"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePublisher captures the raw bytes published to NATS so tests can decode
// the resulting LogEntry.
type fakePublisher struct {
	mu        sync.Mutex
	connected bool
	published [][]byte
}

func (f *fakePublisher) PublishRaw(subject string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	f.published = append(f.published, cp)
	return nil
}

func (f *fakePublisher) IsConnected() bool { return f.connected }

func (f *fakePublisher) entries() []logentry.LogEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]logentry.LogEntry, 0, len(f.published))
	for _, d := range f.published {
		var e logentry.LogEntry
		if err := json.Unmarshal(d, &e); err == nil {
			out = append(out, e)
		}
	}
	return out
}

// waitForEntries polls until at least n entries have been published (publishing
// is fire-and-forget in a goroutine) or the deadline passes.
func (f *fakePublisher) waitForEntries(n int) []logentry.LogEntry {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		e := f.entries()
		if len(e) >= n {
			return e
		}
		time.Sleep(5 * time.Millisecond)
	}
	return f.entries()
}

func TestCaptureWriterParsesFields(t *testing.T) {
	pub := &fakePublisher{connected: true}
	cw := newCaptureWriter("default_component")
	cw.attach(pub, "test_service", 100)

	// A sample zerolog JSON event with standard keys plus extra structured fields.
	event := `{"level":"warn","time":"2026-08-30T00:00:00Z","application":"acme","component":"admin_server","message":"login: invalid credentials","email":"bob@example.com","status":401}`
	n, err := cw.Write([]byte(event))
	require.NoError(t, err)
	assert.Equal(t, len(event), n)

	entries := pub.waitForEntries(1)
	require.Len(t, entries, 1)
	e := entries[0]

	assert.Equal(t, "warn", e.Level)
	assert.Equal(t, "login: invalid credentials", e.Message)
	assert.Equal(t, "test_service", e.Service)
	// Component comes from the event's own "component" field.
	assert.Equal(t, "admin_server", e.Component)

	// Standard keys are stripped; only the extra fields remain.
	require.NotNil(t, e.Fields)
	assert.Equal(t, "bob@example.com", e.Fields["email"])
	assert.EqualValues(t, 401, e.Fields["status"])
	assert.NotContains(t, e.Fields, "level")
	assert.NotContains(t, e.Fields, "message")
	assert.NotContains(t, e.Fields, "time")
	assert.NotContains(t, e.Fields, "application")
	assert.NotContains(t, e.Fields, "component")
}

func TestCaptureWriterDefaultComponentAndNoFields(t *testing.T) {
	pub := &fakePublisher{connected: true}
	cw := newCaptureWriter("fallback_component")
	cw.attach(pub, "svc", 100)

	// No "component" field, and no extra keys beyond the standard ones.
	event := `{"level":"info","time":"2026-08-30T00:00:00Z","message":"hello"}`
	_, err := cw.Write([]byte(event))
	require.NoError(t, err)

	entries := pub.waitForEntries(1)
	require.Len(t, entries, 1)
	e := entries[0]
	assert.Equal(t, "fallback_component", e.Component)
	assert.Equal(t, "hello", e.Message)
	assert.Nil(t, e.Fields)
}

func TestCaptureWriterSkipsWhenPublisherNilOrDisconnected(t *testing.T) {
	// No publisher attached at all.
	cw := newCaptureWriter("c")
	n, err := cw.Write([]byte(`{"level":"info","message":"x"}`))
	require.NoError(t, err)
	assert.Equal(t, len(`{"level":"info","message":"x"}`), n)

	// Publisher present but disconnected.
	pub := &fakePublisher{connected: false}
	cw.attach(pub, "svc", 100)
	_, err = cw.Write([]byte(`{"level":"info","message":"x"}`))
	require.NoError(t, err)
	assert.Empty(t, pub.entries())
}

func TestCaptureWriterInvalidJSONNeverBreaks(t *testing.T) {
	pub := &fakePublisher{connected: true}
	cw := newCaptureWriter("c")
	cw.attach(pub, "svc", 100)

	garbage := []byte("not json at all")
	n, err := cw.Write(garbage)
	require.NoError(t, err)
	assert.Equal(t, len(garbage), n)
	assert.Empty(t, pub.entries())
}

func TestLogRequestPublishesStructuredEntry(t *testing.T) {
	pub := &fakePublisher{connected: true}
	l := NewLogger("acme", "admin_server")
	l.AttachPublisher(pub, "admin_server", 100)

	handler := l.LogRequest(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/things?x=1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// The middleware should echo a request id back on the response.
	assert.NotEmpty(t, rec.Header().Get("X-Request-Id"))

	entries := pub.waitForEntries(1)
	require.NotEmpty(t, entries)
	// Find the access-log entry (there may be a constructor debug line too, but
	// that's emitted before AttachPublisher so it won't be published).
	var access *logentry.LogEntry
	for i := range entries {
		if entries[i].Message == "http request" {
			access = &entries[i]
			break
		}
	}
	require.NotNil(t, access, "expected an http request access log entry")
	require.NotNil(t, access.Fields)
	assert.Equal(t, "GET", access.Fields["method"])
	assert.Equal(t, "/api/v1/things?x=1", access.Fields["uri"])
	assert.EqualValues(t, http.StatusTeapot, access.Fields["status"])
	assert.Contains(t, access.Fields, "clientIP")
	assert.Contains(t, access.Fields, "latencyMs")
	assert.Contains(t, access.Fields, "requestID")
	assert.Equal(t, "info", access.Level)
}

func TestLogRequestHonorsInboundRequestID(t *testing.T) {
	pub := &fakePublisher{connected: true}
	l := NewLogger("acme", "svc")
	l.AttachPublisher(pub, "svc", 100)

	handler := l.LogRequest(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "abc123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, "abc123", rec.Header().Get("X-Request-Id"))
	entries := pub.waitForEntries(1)
	var found bool
	for _, e := range entries {
		if e.Message == "http request" {
			assert.Equal(t, "abc123", e.Fields["requestID"])
			found = true
		}
	}
	assert.True(t, found)
}

func TestNormalizeLevel(t *testing.T) {
	cases := map[string]string{
		"trace": "debug",
		"debug": "debug",
		"info":  "info",
		"warn":  "warn",
		"error": "error",
		"fatal": "fatal",
		"panic": "fatal",
		"bogus": "info",
	}
	for in, want := range cases {
		assert.Equal(t, want, normalizeLevel(in), "normalizeLevel(%q)", in)
	}
}

func TestLevelToString(t *testing.T) {
	cases := map[zerolog.Level]string{
		zerolog.DebugLevel: "debug",
		zerolog.InfoLevel:  "info",
		zerolog.WarnLevel:  "warn",
		zerolog.ErrorLevel: "error",
		zerolog.FatalLevel: "fatal",
		zerolog.PanicLevel: "fatal",
		zerolog.NoLevel:    "info",
	}
	for in, want := range cases {
		assert.Equal(t, want, levelToString(in), "levelToString(%v)", in)
	}
}

func TestSetLevelKeepsCaptureWriter(t *testing.T) {
	pub := &fakePublisher{connected: true}
	l := NewLogger("acme", "svc")
	l.AttachPublisher(pub, "svc", 100)

	// Changing the level must not drop the capture writer from the pipeline.
	l.SetLevel(zerolog.InfoLevel)
	l.Info("after set level")

	entries := pub.waitForEntries(1)
	var found bool
	for _, e := range entries {
		if e.Message == "after set level" {
			found = true
		}
	}
	assert.True(t, found, "capture writer should survive SetLevel")
}
