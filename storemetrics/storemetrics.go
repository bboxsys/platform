// Package storemetrics defines the store metrics entry model used for centralized metrics collection.
package storemetrics

import (
	"crypto/rand"
	"os"
	"time"

	"github.com/bboxsys/store"
	"github.com/oklog/ulid/v2"
)

// StoreMetricsEntry represents a single metrics snapshot published by a service.
type StoreMetricsEntry struct {
	ID        string        `json:"id"`         // ULID for time-ordered keys
	Timestamp int64         `json:"timestamp"`  // Unix millis
	Hostname  string        `json:"hostname"`   // OS hostname of the publishing node
	Service   string        `json:"service"`    // e.g. "admin_server"
	StoreName string        `json:"store_name"` // e.g. "admin.db"
	Metrics   store.Metrics `json:"metrics"`
}

// NATSSubjectPrefix is the base subject for metrics publishing.
// Full subject: sys.metrics.{service}
const NATSSubjectPrefix = "sys.metrics"

// NATSSubjectWildcard matches all service metrics subjects.
const NATSSubjectWildcard = "sys.metrics.>"

// NATSSubject returns the NATS subject for a given service name.
func NATSSubject(service string) string {
	return NATSSubjectPrefix + "." + service
}

// NewStoreMetricsEntry creates a new StoreMetricsEntry with a ULID and current timestamp.
func NewStoreMetricsEntry(service, storeName string, metrics *store.Metrics) *StoreMetricsEntry {
	now := time.Now()
	id := ulid.MustNew(ulid.Timestamp(now), rand.Reader)
	hostname, _ := os.Hostname()
	return &StoreMetricsEntry{
		ID:        id.String(),
		Timestamp: now.UnixMilli(),
		Hostname:  hostname,
		Service:   service,
		StoreName: storeName,
		Metrics:   *metrics,
	}
}
