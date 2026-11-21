// Package usage records what an AI instance spends and reports it to the
// admin server. Every host of package ai (the admin server, a product
// service, a host agent) gets it by default through package ai/instance.
//
// An instance keeps its own records in its own store for a retention
// period, publishes each one over NATS on UsageSubject(instance), and
// publishes a Heartbeat with running totals every HeartbeatInterval on
// HeartbeatSubject(instance). Records that could not be published wait in
// an outbox in the store and are retried on the next heartbeat; the
// collector stores records by ID, so a retry never counts twice. Without a
// NATS connection everything still works and stays local.
//
// The admin server's AI collector subscribes to SubjectWildcard in every
// account (the same per-account connections that collect logs and store
// metrics) and keeps an aggregate store the AI > Usage page reads.
package usage

import (
	"time"

	"github.com/bboxsys/platform/ai"
)

const (
	// SubjectPrefix is the base of every AI usage subject. Agents and
	// services may publish under it by default (user.BaselinePublishSubjects).
	SubjectPrefix = "sys.ai"
	// SubjectWildcard matches every usage and heartbeat subject.
	SubjectWildcard = "sys.ai.>"

	// DefaultRetention is how long an instance keeps its own records.
	DefaultRetention = 30 * 24 * time.Hour
	// DefaultHeartbeatInterval is how often an instance reports its totals.
	DefaultHeartbeatInterval = 60 * time.Second
	// OfflineAfter is how many missed heartbeats mark an instance offline.
	OfflineAfter = 3
)

// UsageSubject is where an instance publishes each Record.
func UsageSubject(instance string) string { return SubjectPrefix + ".usage." + instance }

// HeartbeatSubject is where an instance publishes its Heartbeat.
func HeartbeatSubject(instance string) string { return SubjectPrefix + ".heartbeat." + instance }

// Record is one run as stored and reported: the ai.RunRecord plus where it
// ran. ID is unique (a ULID), so a collector can store records idempotently.
type Record struct {
	ID string `json:"id"`
	// Instance names the AI instance (for example "admin_service"); Host is
	// the machine it runs on. Instance plus Host identifies one running
	// instance.
	Instance string `json:"instance"`
	Service  string `json:"service,omitempty"`
	Host     string `json:"host"`
	ai.RunRecord
	At time.Time `json:"at"`
	// SourceAccount is the NATS account the record arrived from, set by the
	// collector, not by the instance.
	SourceAccount string `json:"source_account,omitempty"`
}

// Totals are cumulative counters.
type Totals struct {
	Runs      int64    `json:"runs"`
	Turns     int64    `json:"turns"`
	ToolCalls int64    `json:"tool_calls"`
	Errors    int64    `json:"errors"`
	Usage     ai.Usage `json:"usage"`
}

// Add accumulates one record.
func (t *Totals) Add(r Record) {
	t.Runs++
	t.Turns += int64(r.Turns)
	t.ToolCalls += int64(r.ToolCalls)
	if r.Error != "" {
		t.Errors++
	}
	t.Usage.Add(r.Usage)
}

// Heartbeat reports that an instance is running and what it has spent.
// Totals cover the records the instance still holds (its retention
// window), so they are comparable across restarts.
type Heartbeat struct {
	Instance string `json:"instance"`
	Service  string `json:"service,omitempty"`
	Host     string `json:"host"`
	// Version is the platform build or a host-supplied version string.
	Version string `json:"version,omitempty"`
	// Provider and Model are the instance's defaults, when it has one.
	Provider  string    `json:"provider,omitempty"`
	Model     string    `json:"model,omitempty"`
	StartedAt time.Time `json:"started_at"`
	At        time.Time `json:"at"`
	// IntervalSeconds is the heartbeat period, so a collector can tell when
	// the instance went quiet.
	IntervalSeconds int    `json:"interval_seconds"`
	Totals          Totals `json:"totals"`
	SourceAccount   string `json:"source_account,omitempty"`
}

// Publisher sends bytes on a NATS subject. *messenger.Client satisfies it.
type Publisher interface {
	PublishRaw(subject string, data []byte) error
}

// Local receives records and heartbeats in-process, for a host that is
// itself the collector (the admin server's own assistant). The admin's
// AIUsageStore satisfies it.
type Local interface {
	PutRecord(r Record) error
	PutHeartbeat(h Heartbeat) error
}
