// Package worker runs job handlers wherever a worker process is listening.
//
// A Worker subscribes to one job queue (job.queue.{service}.{name}) in a
// NATS queue group, so when several processes host the same worker each job
// is delivered to exactly one of them. Handlers run on the subscription's
// goroutine, one job at a time per process; host more processes to run more
// jobs concurrently.
//
//	w := worker.New(&worker.Options{Service: "media", JobName: "thumbnail", MC: mc, Logger: l})
//	w.Run(func(j *job.Job) (string, error) {
//	    var p thumbnailArgs
//	    if err := j.DecodePayload(&p); err != nil { return "", err }
//	    return render(p)
//	})
//
// Run reports started, then complete or error, on job.status.{id}. Subscribe
// is the raw form for handlers that report their own status.
package worker

import (
	"fmt"

	"github.com/bboxsys/platform/job"
	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/platform/messenger"
	"github.com/nats-io/nats.go"
)

// Options configure a Worker. MC is required. QueueGroup defaults to
// messenger.QUEUE_GROUP ("workers"); Logger defaults to a platform/worker
// logger.
type Options struct {
	Service    string
	JobName    string
	MC         *messenger.Client
	Logger     *logger.Logger
	QueueGroup string
}

// Worker takes jobs for one service/name pair from the queue.
type Worker struct {
	Service string `json:"service"`
	JobName string `json:"job_name"`
	group   string
	mc      *messenger.Client
	logger  *logger.Logger
}

// Handler does the work of one job and returns its result text.
type Handler func(j *job.Job) (string, error)

// New builds a Worker; nothing is subscribed until Subscribe or Run.
func New(o *Options) *Worker {
	w := &Worker{
		Service: o.Service,
		JobName: o.JobName,
		group:   o.QueueGroup,
		mc:      o.MC,
		logger:  o.Logger,
	}
	if w.group == "" {
		w.group = messenger.QUEUE_GROUP
	}
	if w.logger == nil {
		w.logger = logger.NewLogger("platform", "worker")
	}
	return w
}

// Subject is the queue subject this worker listens on.
func (w *Worker) Subject() string { return job.QueueSubject(w.Service, w.JobName) }

// Subscribe joins the queue group and hands every delivered job to cb. The
// callback is responsible for reporting status (see DispatchStatus).
func (w *Worker) Subscribe(cb func(j *job.Job)) (*nats.Subscription, error) {
	if w.mc == nil || w.mc.Client == nil {
		return nil, job.ErrNotConnected
	}
	w.logger.InfoE().Str("service", w.Service).Str("job", w.JobName).Str("group", w.group).
		Msg("worker: subscribing to queue")
	return w.mc.Client.QueueSubscribe(w.Subject(), w.group, func(j *job.Job) {
		w.logger.InfoE().Str("service", j.Service).Str("job", j.Name).Str("id", j.ID).
			Msg("worker: received job")
		cb(j)
	})
}

// Run subscribes with a handler whose lifecycle the worker reports: started
// before the handler runs, then complete with its result or error with the
// failure. A handler panic is reported as an error and does not take the
// worker down.
func (w *Worker) Run(h Handler) (*nats.Subscription, error) {
	return w.Subscribe(func(j *job.Job) {
		w.report(j, &job.StatusOptions{State: job.StateStarted, Running: true})
		result, err := w.invoke(h, j)
		if err != nil {
			w.logger.ErrorE().Str("id", j.ID).Str("job", j.Name).Err(err).Msg("worker: job failed")
			w.report(j, &job.StatusOptions{State: job.StateError, Errors: []error{err}})
			return
		}
		w.report(j, &job.StatusOptions{State: job.StateComplete, Result: result})
	})
}

// DispatchStatus publishes a status update for j through the worker's client.
func (w *Worker) DispatchStatus(j *job.Job, opts *job.StatusOptions) error {
	return j.DispatchStatus(w.mc, opts)
}

func (w *Worker) report(j *job.Job, opts *job.StatusOptions) {
	if err := w.DispatchStatus(j, opts); err != nil {
		w.logger.ErrorE().Str("id", j.ID).Str("state", string(opts.State)).Err(err).
			Msg("worker: error publishing job status")
	}
}

func (w *Worker) invoke(h Handler, j *job.Job) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &panicError{value: r}
		}
	}()
	return h(j)
}

type panicError struct{ value interface{} }

func (e *panicError) Error() string { return "worker: handler panic: " + describe(e.value) }

func describe(v interface{}) string {
	if err, ok := v.(error); ok {
		return err.Error()
	}
	return fmt.Sprint(v)
}
