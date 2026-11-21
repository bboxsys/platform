package queue

import (
	"errors"
	"fmt"

	"github.com/bboxsys/platform/job"
	"github.com/bboxsys/store"
	badger "github.com/dgraph-io/badger/v4"
)

// ErrNotFound is returned when a job ID is unknown to the store.
var ErrNotFound = errors.New("queue: job not found")

// Store records every job request and the latest status of each job.
type Store struct {
	*store.Store
}

// StoreOptions locate the queue store on disk.
type StoreOptions struct {
	Location    string `json:"location"`
	Application string `json:"application"`
	Component   string `json:"component"`
}

// Record is a stored job with its latest status, if a worker reported one.
type Record struct {
	Job    *job.Job    `json:"job"`
	Status *job.Status `json:"status,omitempty"`
}

func jobKey(id string) string       { return fmt.Sprintf("job:%s", id) }
func jobStatusKey(id string) string { return fmt.Sprintf("job_status:%s", id) }

// NewStore opens (or creates) the queue store.
func NewStore(options *StoreOptions) (*Store, error) {
	s, err := store.NewStore(&store.Options{
		Location:    options.Location,
		Application: options.Application,
		Component:   options.Component,
	})
	if err != nil {
		return nil, err
	}
	return &Store{s}, nil
}

// PutJob records a job request.
func (s *Store) PutJob(j *job.Job) error {
	return s.PutOne(jobKey(j.ID), j)
}

// PutJobStatus records the latest status of a job, replacing any earlier one.
func (s *Store) PutJobStatus(status *job.Status) error {
	return s.PutOne(jobStatusKey(status.JobID), status)
}

// GetJob returns a stored job request.
func (s *Store) GetJob(id string) (*job.Job, error) {
	var j job.Job
	if err := s.GetOne(jobKey(id), &j); err != nil {
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &j, nil
}

// GetJobStatus returns the latest status of a job, or nil when no worker
// has reported one yet.
func (s *Store) GetJobStatus(id string) (*job.Status, error) {
	var st job.Status
	if err := s.GetOne(jobStatusKey(id), &st); err != nil {
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &st, nil
}

// GetRecord returns a job with its latest status.
func (s *Store) GetRecord(id string) (*Record, error) {
	j, err := s.GetJob(id)
	if err != nil {
		return nil, err
	}
	st, err := s.GetJobStatus(id)
	if err != nil {
		return nil, err
	}
	return &Record{Job: j, Status: st}, nil
}

// ListRecords returns every stored job with its latest status, oldest first
// (job IDs are ULIDs, so key order is creation order).
func (s *Store) ListRecords() ([]*Record, error) {
	var jobs []*job.Job
	if err := s.Get(jobKey(""), &jobs); err != nil {
		return nil, err
	}
	var statuses []*job.Status
	if err := s.Get(jobStatusKey(""), &statuses); err != nil {
		return nil, err
	}
	latest := make(map[string]*job.Status, len(statuses))
	for _, st := range statuses {
		latest[st.JobID] = st
	}
	records := make([]*Record, 0, len(jobs))
	for _, j := range jobs {
		records = append(records, &Record{Job: j, Status: latest[j.ID]})
	}
	return records, nil
}
