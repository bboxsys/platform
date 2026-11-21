// Package queue is the job relay service: it records every job request and
// status update in a store and republishes requests to the worker queue, so
// that job history survives and workers never need to see job.request.*.
//
// Subscribes to:
//   - job.request.*.*   (stores the job, republishes it with Job.Enqueue)
//   - job.status.*      (stores the latest status per job)
//
// Publishes to:
//   - job.queue.{service}.{name}
//
// QueueServer wraps the relay in an authenticated HTTPS service with a small
// REST surface for submitting jobs and reading their status. Products that
// want the bookkeeping run one QueueServer per deployment; products that do
// not can call Job.Enqueue directly and skip this package.
package queue

import (
	"github.com/bboxsys/platform/job"
	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/platform/messenger"
	"github.com/nats-io/nats.go"
)

// Queue is the relay between job requests and the worker queue.
type Queue struct {
	Store           *Store
	MessengerClient *messenger.Client
	Logger          *logger.Logger
	subs            []*nats.Subscription
}

// Start subscribes to job requests and status updates.
func (q *Queue) Start() error {
	q.Logger.Info("queue: starting relay")
	if err := q.SubscribeJobRequest(); err != nil {
		return err
	}
	return q.SubscribeJobStatus()
}

// Stop drops the relay's subscriptions; the store and client stay open.
func (q *Queue) Stop() {
	for _, sub := range q.subs {
		if err := sub.Unsubscribe(); err != nil {
			q.Logger.ErrorE().Err(err).Msg("queue: error unsubscribing")
		}
	}
	q.subs = nil
}

// SubscribeJobRequest stores every job request and hands it to the workers.
func (q *Queue) SubscribeJobRequest() error {
	q.Logger.Info("queue: listening for job requests")
	sub, err := job.SubscribeRequests(q.MessengerClient, func(j *job.Job) {
		q.Logger.InfoE().Str("service", j.Service).Str("job", j.Name).Str("id", j.ID).
			Msg("queue: received job request")
		if err := q.Store.PutJob(j); err != nil {
			q.Logger.ErrorE().Str("id", j.ID).Err(err).Msg("queue: error storing job")
		}
		if err := j.Enqueue(q.MessengerClient); err != nil {
			q.Logger.ErrorE().Str("id", j.ID).Err(err).Msg("queue: error publishing job to worker queue")
		}
	})
	if err != nil {
		return err
	}
	q.subs = append(q.subs, sub)
	return nil
}

// SubscribeJobStatus stores the latest status of every job.
func (q *Queue) SubscribeJobStatus() error {
	q.Logger.Info("queue: listening for job status updates")
	sub, err := job.SubscribeStatuses(q.MessengerClient, func(status *job.Status) {
		q.Logger.InfoE().Str("id", status.JobID).Str("state", string(status.State)).
			Msg("queue: received job status update")
		if err := q.Store.PutJobStatus(status); err != nil {
			q.Logger.ErrorE().Str("id", status.JobID).Err(err).Msg("queue: error storing job status")
		}
	})
	if err != nil {
		return err
	}
	q.subs = append(q.subs, sub)
	return nil
}
