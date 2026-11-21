package logger

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bboxsys/platform/logentry"
)

// captureWriter is an io.Writer that sits in a zerolog MultiLevelWriter and
// receives the raw JSON for every log event. Because it parses the full JSON
// (unlike a zerolog.Hook, whose Run only sees the message string), it can carry
// the event's structured fields into a logentry.LogEntry and publish it to
// NATS. It is fire-and-forget and never breaks logging: Write always reports
// success even on a parse or publish error.
type captureWriter struct {
	// publisher is set atomically once AttachPublisher runs; nil means the
	// capture path is inert and events are only written to the console.
	publisher atomic.Pointer[publisherRef]

	// service/component default and rate limit are only read/written from
	// AttachPublisher (before publishing begins), so a plain field plus the
	// atomic publisher gate is sufficient.
	service          string
	defaultComponent string

	// Rate limiting (mirrors NATSHook).
	count       atomic.Int64
	windowStart atomic.Int64 // Unix nanos
	maxPerSec   atomic.Int64
	mu          sync.Mutex
	dropped     atomic.Int64
}

// publisherRef wraps a NATSPublisher so it can be stored in an atomic.Pointer.
type publisherRef struct {
	pub NATSPublisher
}

// standardKeys are the zerolog/logger-managed keys that are lifted into
// dedicated LogEntry fields and therefore removed from the Fields map.
var standardKeys = []string{"level", "message", "time", "application", "component"}

// newCaptureWriter creates a capture writer with the given default component.
// The publisher is nil until AttachPublisher is called.
func newCaptureWriter(defaultComponent string) *captureWriter {
	cw := &captureWriter{defaultComponent: defaultComponent}
	cw.maxPerSec.Store(100)
	cw.windowStart.Store(time.Now().UnixNano())
	return cw
}

// attach configures the publisher, service name, and rate limit. It is called
// from Logger.AttachPublisher.
func (cw *captureWriter) attach(pub NATSPublisher, service string, maxPerSec int64) {
	if maxPerSec <= 0 {
		maxPerSec = 100
	}
	cw.service = service
	cw.maxPerSec.Store(maxPerSec)
	cw.windowStart.Store(time.Now().UnixNano())
	cw.publisher.Store(&publisherRef{pub: pub})
}

// Write implements io.Writer. It parses one zerolog JSON event, builds a
// logentry.LogEntry carrying the remaining keys as Fields, and publishes it
// fire-and-forget. It always returns len(p), nil so logging is never broken.
func (cw *captureWriter) Write(p []byte) (int, error) {
	ref := cw.publisher.Load()
	if ref == nil || ref.pub == nil || !ref.pub.IsConnected() {
		return len(p), nil
	}
	if !cw.allowEntry() {
		return len(p), nil
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(p, &raw); err != nil {
		// Not valid JSON (shouldn't happen with zerolog) -- never break logging.
		return len(p), nil
	}

	// Extract level and message.
	level := "info"
	if lvl, ok := raw["level"].(string); ok && lvl != "" {
		level = normalizeLevel(lvl)
	}
	message, _ := raw["message"].(string)

	// Component comes from the event's "component" field, falling back to the
	// writer's configured default.
	component := cw.defaultComponent
	if comp, ok := raw["component"].(string); ok && comp != "" {
		component = comp
	}

	// Remove the standard keys, leaving the remainder as structured fields.
	for _, k := range standardKeys {
		delete(raw, k)
	}
	var fields map[string]interface{}
	if len(raw) > 0 {
		fields = raw
	}

	entry := logentry.NewLogEntryWithFields(cw.service, level, message, component, fields)
	data, err := json.Marshal(entry)
	if err != nil {
		return len(p), nil
	}

	subject := logentry.NATSSubject(cw.service)
	pub := ref.pub
	// Fire-and-forget: don't block the caller.
	go func() {
		_ = pub.PublishRaw(subject, data)
	}()

	return len(p), nil
}

// allowEntry returns true if this entry is within the per-second rate limit.
func (cw *captureWriter) allowEntry() bool {
	now := time.Now().UnixNano()
	windowStart := cw.windowStart.Load()
	elapsed := now - windowStart

	// Reset window every second.
	if elapsed >= int64(time.Second) {
		cw.mu.Lock()
		if time.Now().UnixNano()-cw.windowStart.Load() >= int64(time.Second) {
			cw.dropped.Swap(0)
			cw.count.Store(0)
			cw.windowStart.Store(time.Now().UnixNano())
		}
		cw.mu.Unlock()
	}

	current := cw.count.Add(1)
	if current > cw.maxPerSec.Load() {
		cw.dropped.Add(1)
		return false
	}
	return true
}

// normalizeLevel maps a zerolog level string to the canonical set used by
// LogEntry (matching levelToString). Zerolog emits "panic" for panic-level
// events; both panic and fatal collapse to "fatal".
func normalizeLevel(lvl string) string {
	switch lvl {
	case "trace":
		return "debug"
	case "debug", "info", "warn", "error":
		return lvl
	case "fatal", "panic":
		return "fatal"
	default:
		return "info"
	}
}
