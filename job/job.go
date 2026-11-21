// Package job defines the unit of asynchronous work that a service hands to
// workers over NATS, the status a worker reports back, and the subjects the
// exchange runs on. Package worker is the receiving side.
//
// Subjects, all inside the tenant account the messenger client is bound to:
//
//	job.request.{service}.{name}  a producer asks for a job to run (Dispatch)
//	job.queue.{service}.{name}    a job offered to the worker queue group;
//	                              exactly one subscribed worker receives it
//	job.status.{id}               a worker reports progress for one job
//
// A queue relay (package queue is one) subscribes to every request,
// records it, and republishes it with Enqueue. A producer that does not need
// that bookkeeping can call Enqueue itself and skip the relay.
package job

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bboxsys/platform/messenger"
	"github.com/nats-io/nats.go"
	"github.com/oklog/ulid/v2"
)

const (
	// RequestWildcard matches every job request; a queue relay subscribes here.
	RequestWildcard = "job.request.*.*"
	// StatusWildcard matches every status update; a queue relay records them.
	StatusWildcard = "job.status.*"
)

// ErrNotConnected is returned when the messenger client has no NATS connection.
var ErrNotConnected = errors.New("job: messenger client is not connected")

// Job is the request that travels from producer to worker. Payload is any
// JSON-encodable value; on the receiving side it decodes to the generic JSON
// types, so workers call DecodePayload to get their own struct back.
type Job struct {
	ID      string      `json:"id"`
	Service string      `json:"service"`
	Name    string      `json:"name"`
	Payload interface{} `json:"payload"`
}

// State is the lifecycle stage a Status reports.
type State string

const (
	StateWaiting   State = "waiting"
	StateStarted   State = "started"
	StateComplete  State = "complete"
	StateCancelled State = "cancelled"
	StateError     State = "error"
)

// StatusOptions is what a worker supplies for one status update.
type StatusOptions struct {
	State   State
	Running bool
	Result  string
	Errors  []error
}

// Status is one progress report for a job, published on job.status.{id}.
type Status struct {
	JobID     string    `json:"job_id"`
	Service   string    `json:"service"`
	JobName   string    `json:"job_name"`
	Timestamp time.Time `json:"timestamp"`
	State     State     `json:"state"`
	Running   bool      `json:"running"`
	Result    string    `json:"result"`
	Errors    []string  `json:"errors,omitempty"`
}

// NewID returns a fresh job identifier (a ULID, so IDs sort by creation time).
func NewID() string {
	return ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String()
}

// New builds a job request for service/name with a fresh ID.
func New(service, name string, payload interface{}) *Job {
	return &Job{
		ID:      NewID(),
		Service: service,
		Name:    name,
		Payload: payload,
	}
}

// RequestSubject is where producers publish a job for service/name.
func RequestSubject(service, name string) string {
	return fmt.Sprintf("job.request.%s.%s", service, name)
}

// QueueSubject is where workers for service/name take jobs from.
func QueueSubject(service, name string) string {
	return fmt.Sprintf("job.queue.%s.%s", service, name)
}

// StatusSubject carries the status updates of one job.
func StatusSubject(id string) string {
	return fmt.Sprintf("job.status.%s", id)
}

// RequestSubject is the subject Dispatch publishes this job on.
func (j *Job) RequestSubject() string { return RequestSubject(j.Service, j.Name) }

// QueueSubject is the subject Enqueue publishes this job on.
func (j *Job) QueueSubject() string { return QueueSubject(j.Service, j.Name) }

// StatusSubject is the subject this job's status updates travel on.
func (j *Job) StatusSubject() string { return StatusSubject(j.ID) }

// DecodePayload converts the generic decoded payload into v (a pointer to the
// producer's payload type) by round-tripping it through JSON.
func (j *Job) DecodePayload(v interface{}) error {
	data, err := json.Marshal(j.Payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// Dispatch publishes the job request for a queue relay to pick up.
func (j *Job) Dispatch(mc *messenger.Client) error {
	return publish(mc, j.RequestSubject(), j)
}

// Enqueue offers the job directly to the worker queue, bypassing any relay.
func (j *Job) Enqueue(mc *messenger.Client) error {
	return publish(mc, j.QueueSubject(), j)
}

// NewStatus stamps a status update for the job.
func NewStatus(j *Job, opts *StatusOptions) *Status {
	if opts == nil {
		opts = &StatusOptions{}
	}
	s := &Status{
		JobID:     j.ID,
		Service:   j.Service,
		JobName:   j.Name,
		Timestamp: time.Now().UTC(),
		State:     opts.State,
		Running:   opts.Running,
		Result:    opts.Result,
	}
	for _, err := range opts.Errors {
		if err != nil {
			s.Errors = append(s.Errors, err.Error())
		}
	}
	return s
}

// DispatchStatus publishes a status update for the job.
func (j *Job) DispatchStatus(mc *messenger.Client, opts *StatusOptions) error {
	return publish(mc, j.StatusSubject(), NewStatus(j, opts))
}

// SubscribeStatus delivers every status update for this job to cb. Callers
// unsubscribe through the returned subscription when they stop caring.
func (j *Job) SubscribeStatus(mc *messenger.Client, cb func(*Status)) (*nats.Subscription, error) {
	if !connected(mc) {
		return nil, ErrNotConnected
	}
	return mc.Client.Subscribe(j.StatusSubject(), cb)
}

// SubscribeRequests delivers every job request to cb; a queue relay uses it.
func SubscribeRequests(mc *messenger.Client, cb func(*Job)) (*nats.Subscription, error) {
	if !connected(mc) {
		return nil, ErrNotConnected
	}
	return mc.Client.Subscribe(RequestWildcard, cb)
}

// SubscribeStatuses delivers every status update of every job to cb.
func SubscribeStatuses(mc *messenger.Client, cb func(*Status)) (*nats.Subscription, error) {
	if !connected(mc) {
		return nil, ErrNotConnected
	}
	return mc.Client.Subscribe(StatusWildcard, cb)
}

func connected(mc *messenger.Client) bool {
	return mc != nil && mc.Client != nil
}

func publish(mc *messenger.Client, subject string, msg interface{}) error {
	if !connected(mc) {
		return ErrNotConnected
	}
	return mc.Client.Publish(subject, msg)
}
