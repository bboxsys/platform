// Package logentry defines the log entry model used for centralized log collection.
package logentry

import (
	"crypto/rand"
	"os"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// LogEntry represents a single log message published by a service.
type LogEntry struct {
	ID        string                 `json:"id"`        // ULID for time-ordered keys
	Timestamp int64                  `json:"timestamp"` // Unix millis
	Service   string                 `json:"service"`   // e.g. "admin_server", "queue_server"
	Level     string                 `json:"level"`     // "debug","info","warn","error","fatal"
	Message   string                 `json:"message"`
	Component string                 `json:"component"`          // sub-component within service
	Hostname  string                 `json:"hostname,omitempty"` // OS hostname of the publishing node
	Fields    map[string]interface{} `json:"fields,omitempty"`   // structured fields from the log call site
}

var (
	hostnameOnce sync.Once
	hostname     string
)

// localHostname returns the OS hostname, resolved once per process.
func localHostname() string {
	hostnameOnce.Do(func() {
		hostname, _ = os.Hostname()
	})
	return hostname
}

// NATSSubjectPrefix is the base subject for log publishing.
// Full subject: sys.log.{service}
const NATSSubjectPrefix = "sys.log"

// NATSSubjectWildcard matches all service log subjects.
const NATSSubjectWildcard = "sys.log.>"

// NATSSubject returns the NATS subject for a given service name.
func NATSSubject(service string) string {
	return NATSSubjectPrefix + "." + service
}

// NewLogEntry creates a new LogEntry with a ULID and current timestamp.
func NewLogEntry(service, level, message, component string) *LogEntry {
	return NewLogEntryWithFields(service, level, message, component, nil)
}

// NewLogEntryWithFields creates a new LogEntry with structured fields attached.
// A nil or empty fields map is omitted from the serialized entry.
func NewLogEntryWithFields(service, level, message, component string, fields map[string]interface{}) *LogEntry {
	now := time.Now()
	id := ulid.MustNew(ulid.Timestamp(now), rand.Reader)
	if len(fields) == 0 {
		fields = nil
	}
	return &LogEntry{
		ID:        id.String(),
		Timestamp: now.UnixMilli(),
		Service:   service,
		Level:     level,
		Message:   message,
		Component: component,
		Hostname:  localHostname(),
		Fields:    fields,
	}
}
