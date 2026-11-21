package logger

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bboxsys/platform/logentry"
	"github.com/rs/zerolog"
)

// NATSPublisher is an interface for publishing log entries to NATS.
// This avoids a direct dependency on the messenger package.
type NATSPublisher interface {
	PublishRaw(subject string, data []byte) error
	IsConnected() bool
}

// NATSHook is a zerolog Hook that publishes log events to NATS.
type NATSHook struct {
	publisher NATSPublisher
	service   string
	component string

	// Rate limiting
	count     atomic.Int64
	windowStart atomic.Int64 // Unix nanos
	maxPerSec int64
	mu        sync.Mutex
	dropped   atomic.Int64
}

// NATSHookOptions configures the NATS log hook.
type NATSHookOptions struct {
	Publisher   NATSPublisher
	Service     string
	Component   string
	MaxPerSec   int64 // Max log entries per second (default 100)
}

// NewNATSHook creates a new zerolog hook that publishes to NATS.
func NewNATSHook(opts *NATSHookOptions) *NATSHook {
	maxPerSec := opts.MaxPerSec
	if maxPerSec <= 0 {
		maxPerSec = 100
	}
	h := &NATSHook{
		publisher: opts.Publisher,
		service:   opts.Service,
		component: opts.Component,
		maxPerSec: maxPerSec,
	}
	h.windowStart.Store(time.Now().UnixNano())
	return h
}

// Run implements zerolog.Hook. It is called for every log event.
func (h *NATSHook) Run(e *zerolog.Event, level zerolog.Level, msg string) {
	// Don't publish if no publisher or not connected
	if h.publisher == nil || !h.publisher.IsConnected() {
		return
	}

	// Rate limit check
	if !h.allowEntry() {
		return
	}

	entry := logentry.NewLogEntry(
		h.service,
		levelToString(level),
		msg,
		h.component,
	)

	data, err := json.Marshal(entry)
	if err != nil {
		return
	}

	subject := logentry.NATSSubject(h.service)
	// Fire-and-forget: don't block the caller
	go func() {
		_ = h.publisher.PublishRaw(subject, data)
	}()
}

// allowEntry returns true if this entry is within the rate limit.
func (h *NATSHook) allowEntry() bool {
	now := time.Now().UnixNano()
	windowStart := h.windowStart.Load()
	elapsed := now - windowStart

	// Reset window every second
	if elapsed >= int64(time.Second) {
		h.mu.Lock()
		// Double-check under lock
		if time.Now().UnixNano()-h.windowStart.Load() >= int64(time.Second) {
			prevDropped := h.dropped.Swap(0)
			h.count.Store(0)
			h.windowStart.Store(time.Now().UnixNano())

			// Log dropped count from previous window (non-recursively)
			if prevDropped > 0 {
				// We just log to stderr; the hook won't re-enter since we check count below
			}
		}
		h.mu.Unlock()
	}

	current := h.count.Add(1)
	if current > h.maxPerSec {
		h.dropped.Add(1)
		return false
	}
	return true
}

func levelToString(level zerolog.Level) string {
	switch level {
	case zerolog.DebugLevel:
		return "debug"
	case zerolog.InfoLevel:
		return "info"
	case zerolog.WarnLevel:
		return "warn"
	case zerolog.ErrorLevel:
		return "error"
	case zerolog.FatalLevel:
		return "fatal"
	case zerolog.PanicLevel:
		return "fatal"
	default:
		return "info"
	}
}
