// Logging package for unified JSON logging.  Will look like this:
// {"time": xxx, "level": xxx, "component": xxx, "message": xxx, "key": xxx}
package logger

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"github.com/bboxsys/platform/clientip"
)

// Levels are:
// - Debug 0
// - Info  1
// - Warn  2
// - Error 3
// - Panic 4
// - Fatal 5

type LoggerConfig struct {
	LogLevel *zerolog.Level
	Outputs  []interface{}
}

type Logger struct {
	logger atomic.Pointer[zerolog.Logger]
	// capture receives the raw JSON for every event (via a MultiLevelWriter)
	// so structured fields can be published to NATS. It survives SetLevel
	// because .Level() returns a copy that keeps the same output writer.
	capture *captureWriter
}

// Will start with console output and go from there
func NewLogger(application string, component string) *Logger {
	cw := newCaptureWriter(component)
	// Human-readable console output for stderr, plus the capture writer that
	// receives the raw JSON event so structured fields can be lifted out.
	mw := zerolog.MultiLevelWriter(zerolog.ConsoleWriter{Out: os.Stderr}, cw)
	l := zerolog.New(mw).
		With().
		Timestamp().
		Str("application", application).
		Str("component", component).
		Logger()
	logger := &Logger{capture: cw}
	logger.logger.Store(&l)
	logger.Debug("constructed a logger")
	return logger
}

// Event wraps a *zerolog.Event with a small chainable, typed-field API so
// callers can attach structured fields instead of interpolating them into the
// message string, e.g. l.WarnE().Str("email", email).Str("clientIP", ip).
// Msg("login: invalid credentials"). The terminal Msg call emits the log line.
type Event struct{ e *zerolog.Event }

// DebugE/InfoE/WarnE/ErrorE start a structured event at the given level.
func (l *Logger) DebugE() *Event { return &Event{e: l.logger.Load().Debug()} }
func (l *Logger) InfoE() *Event  { return &Event{e: l.logger.Load().Info()} }
func (l *Logger) WarnE() *Event  { return &Event{e: l.logger.Load().Warn()} }
func (l *Logger) ErrorE() *Event { return &Event{e: l.logger.Load().Error()} }

func (ev *Event) Str(key, val string) *Event            { ev.e = ev.e.Str(key, val); return ev }
func (ev *Event) Strs(key string, vals []string) *Event { ev.e = ev.e.Strs(key, vals); return ev }
func (ev *Event) Int(key string, val int) *Event        { ev.e = ev.e.Int(key, val); return ev }
func (ev *Event) Int64(key string, v int64) *Event      { ev.e = ev.e.Int64(key, v); return ev }
func (ev *Event) Uint64(key string, v uint64) *Event    { ev.e = ev.e.Uint64(key, v); return ev }
func (ev *Event) Float64(key string, v float64) *Event  { ev.e = ev.e.Float64(key, v); return ev }
func (ev *Event) Bool(key string, val bool) *Event      { ev.e = ev.e.Bool(key, val); return ev }
func (ev *Event) Time(key string, t time.Time) *Event   { ev.e = ev.e.Time(key, t); return ev }

// Any adds an arbitrary value under key, reflected into structured fields.
// Use it for the %v/%+v cases where the value is not an error and has no
// dedicated typed method above.
func (ev *Event) Any(key string, val interface{}) *Event { ev.e = ev.e.Interface(key, val); return ev }

// Dur adds a duration in milliseconds under key+"Ms".
func (ev *Event) Dur(key string, d time.Duration) *Event {
	ev.e = ev.e.Int64(key+"Ms", d.Milliseconds())
	return ev
}

// Err adds the error under the standard "error" field (no-op for a nil error).
func (ev *Event) Err(err error) *Event {
	if err != nil {
		ev.e = ev.e.Err(err)
	}
	return ev
}

// Fields adds an arbitrary set of fields.
func (ev *Event) Fields(fields map[string]interface{}) *Event {
	ev.e = ev.e.Fields(fields)
	return ev
}

// Msg emits the event with the given message. The Event must not be reused.
func (ev *Event) Msg(msg string) { ev.e.Msg(msg) }

func (l *Logger) Debug(msg string) {
	l.logger.Load().Debug().Msg(msg)
}
func (l *Logger) Info(msg string) {
	l.logger.Load().Info().Msg(msg)
}
func (l *Logger) Warn(msg string) {
	l.logger.Load().Warn().Msg(msg)
}
func (l *Logger) Error(msg string) {
	l.logger.Load().Error().Msg(msg)
}
func (l *Logger) Panic(msg string) {
	l.logger.Load().Panic().Msg(msg)
}
func (l *Logger) Fatal(msg string) {
	l.logger.Load().Fatal().Msg(msg)
}

// statusRecorder wraps http.ResponseWriter to capture the status code written
// so the access log can report it. It defaults to 200 when WriteHeader is
// never called explicitly.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

// Hijack forwards to the underlying ResponseWriter so connection hijacking
// (WebSocket upgrades) keeps working through this wrapper. Without it,
// gorilla/websocket's Upgrade fails with "response does not implement
// http.Hijacker" because the embedded interface value does not expose Hijack.
func (sr *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := sr.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, errors.New("underlying ResponseWriter does not support hijacking")
}

// Flush forwards to the underlying ResponseWriter so streaming/SSE responses
// are not buffered by this wrapper.
func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if sr.status == 0 {
		sr.status = http.StatusOK
	}
	return sr.ResponseWriter.Write(b)
}

// newRequestID returns a short random hex identifier for correlating logs.
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// LogRequest is HTTP middleware that emits a structured access log for each
// request with typed fields (method, uri, status, clientIP, latencyMs,
// requestID) instead of an interpolated string. It honors an inbound
// X-Request-Id header and echoes the request id back on the response so it can
// be correlated across services and in the admin Logs UI.
func (l *Logger) LogRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-Id")
		if requestID == "" {
			requestID = newRequestID()
		}
		w.Header().Set("X-Request-Id", requestID)

		sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sr, r)
		latency := time.Since(start)

		l.InfoE().
			Str("method", r.Method).
			Str("uri", r.URL.String()).
			Int("status", sr.status).
			Str("clientIP", clientip.FromRequest(r).String()).
			Dur("latency", latency).
			Str("requestID", requestID).
			Msg("http request")
	})
}

// ZeroLogger returns the underlying zerolog.Logger for packages that need it
func (l *Logger) ZeroLogger() zerolog.Logger {
	return *l.logger.Load()
}

// SetLevel changes the minimum log level at runtime.
func (l *Logger) SetLevel(level zerolog.Level) {
	updated := l.logger.Load().Level(level)
	l.logger.Store(&updated)
}

// AttachHook adds a hook to this Logger in place and returns the same Logger.
// This mutates the existing Logger so that any code holding a reference
// (e.g., middleware closures) sees the updated logger with the hook.
func (l *Logger) AttachHook(hook zerolog.Hook) *Logger {
	updated := l.logger.Load().Hook(hook)
	l.logger.Store(&updated)
	return l
}

// AttachPublisher configures this Logger's capture writer to publish every
// log event (with its structured fields) to NATS under the given service name,
// rate-limited to maxPerSec entries per second. It returns the same Logger.
// This is the structured replacement for AttachHook on the NATS log path: the
// capture writer is part of the MultiLevelWriter created in NewLogger, so it
// survives SetLevel and needs no re-attach when the level changes.
func (l *Logger) AttachPublisher(pub NATSPublisher, service string, maxPerSec int64) *Logger {
	if l.capture != nil {
		l.capture.attach(pub, service, maxPerSec)
	}
	return l
}

// NewLoggerWithHook creates a logger with a hook pre-attached.
func NewLoggerWithHook(application string, component string, hook zerolog.Hook) *Logger {
	base := NewLogger(application, component)
	return base.AttachHook(hook)
}
