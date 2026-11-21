package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/bboxsys/platform/ai"
	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/store"
	"github.com/dgraph-io/badger/v4"
	"github.com/oklog/ulid/v2"
)

const (
	// RecordPrefix is the store key prefix of an instance's own records,
	// followed by the record ID. ULIDs sort by time, so key order is
	// time order.
	RecordPrefix = "ai_usage:"
	// OutboxPrefix is the store key prefix of records that still have to
	// be published.
	OutboxPrefix = "ai_usage_outbox:"
	// OutboxFlushLimit bounds the outbox records published per heartbeat.
	OutboxFlushLimit = 500
	// publishQueue is the capacity of the asynchronous publish channel;
	// a record that does not fit goes to the outbox.
	publishQueue = 256
)

var instancePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// RecorderOptions configures a Recorder.
type RecorderOptions struct {
	// Instance names the AI instance; it is the last token of the NATS
	// subjects, so it must match [A-Za-z0-9_-]+. Required.
	Instance string
	// Service is the hosting service, when there is one.
	Service string
	// Host is the machine name; empty uses os.Hostname.
	Host string
	// Version is reported in every Heartbeat.
	Version string
	// Store keeps the records and the outbox. Required.
	Store *store.Store
	// Publisher sends records and heartbeats over NATS. With neither a
	// Publisher nor a Local, every record waits in the outbox until a
	// restart with a publisher sends it. A host that is its own collector
	// (Local set, no Publisher) keeps no outbox: Local already has the record.
	Publisher Publisher
	// Local, when set, receives every record and heartbeat in-process.
	Local Local
	// Retention is how long records (and outbox entries) are kept; zero
	// means DefaultRetention.
	Retention time.Duration
	// HeartbeatInterval is the heartbeat period; zero means
	// DefaultHeartbeatInterval.
	HeartbeatInterval time.Duration
	// Provider and Model are the instance's defaults, reported in the
	// Heartbeat.
	Provider string
	Model    string
	// Logger receives operational lines; nil is silent.
	Logger *logger.Logger
}

// Recorder stores, totals, and reports the runs of one AI instance. It
// implements ai.UsageSink. A Recorder records and stores whether or not it
// was started; Start adds the heartbeat and the outbox retry, and Close
// stops them and releases the publishing goroutine.
type Recorder struct {
	opts RecorderOptions

	// mu guards totals and orders store writes against the heartbeat's
	// recount, so a record is never counted twice or missed.
	mu     sync.Mutex
	totals Totals

	// pubMu guards closing pub against concurrent sends.
	pubMu   sync.RWMutex
	pub     chan Record
	pubDone chan struct{}
	closed  bool

	lifeMu    sync.Mutex
	started   bool
	stopped   bool
	startedAt time.Time
	stop      chan struct{}
	loopDone  chan struct{}
}

var _ ai.UsageSink = (*Recorder)(nil)

// NewRecorder validates o, applies the defaults, and starts the goroutine
// that publishes records.
func NewRecorder(o RecorderOptions) (*Recorder, error) {
	if !instancePattern.MatchString(o.Instance) {
		return nil, fmt.Errorf("usage: instance %q must match [A-Za-z0-9_-]+", o.Instance)
	}
	if o.Store == nil {
		return nil, errors.New("usage: store is required")
	}
	if o.Host == "" {
		h, err := os.Hostname()
		if err != nil || h == "" {
			h = "unknown"
		}
		o.Host = h
	}
	if o.Retention <= 0 {
		o.Retention = DefaultRetention
	}
	if o.HeartbeatInterval <= 0 {
		o.HeartbeatInterval = DefaultHeartbeatInterval
	}
	r := &Recorder{
		opts:    o,
		pub:     make(chan Record, publishQueue),
		pubDone: make(chan struct{}),
		stop:    make(chan struct{}),
	}
	go r.publishLoop()
	return r, nil
}

// Options returns the options with their defaults applied.
func (r *Recorder) Options() RecorderOptions { return r.opts }

// RecordRun stores a run, adds it to the totals, hands it to Local, and
// queues it for publishing. It never waits on the network: a record that
// cannot be queued goes to the outbox and is retried on the next heartbeat.
func (r *Recorder) RecordRun(_ context.Context, run ai.RunRecord) {
	rec := Record{
		ID:        ulid.Make().String(),
		Instance:  r.opts.Instance,
		Service:   r.opts.Service,
		Host:      r.opts.Host,
		RunRecord: run,
		At:        time.Now().UTC(),
	}
	r.mu.Lock()
	if err := r.opts.Store.PutOneWithTTL(RecordPrefix+rec.ID, rec, r.opts.Retention); err != nil {
		r.logErr(err, "usage: store record")
	}
	r.totals.Add(rec)
	r.mu.Unlock()

	if r.opts.Local != nil {
		if err := r.opts.Local.PutRecord(rec); err != nil {
			r.logErr(err, "usage: local record")
		}
	}

	switch {
	case r.opts.Publisher != nil:
		if !r.enqueue(rec) {
			r.toOutbox(rec)
		}
	case r.opts.Local == nil:
		r.toOutbox(rec)
	}
}

// enqueue hands rec to the publishing goroutine without blocking and
// reports whether it was taken.
func (r *Recorder) enqueue(rec Record) bool {
	r.pubMu.RLock()
	defer r.pubMu.RUnlock()
	if r.closed {
		return false
	}
	select {
	case r.pub <- rec:
		return true
	default:
		return false
	}
}

func (r *Recorder) publishLoop() {
	defer close(r.pubDone)
	for rec := range r.pub {
		if err := r.publishRecord(rec); err != nil {
			r.logDebug(err, "usage: publish record; kept in the outbox")
			r.toOutbox(rec)
		}
	}
}

func (r *Recorder) publishRecord(rec Record) error {
	if r.opts.Publisher == nil {
		return errors.New("no publisher")
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return r.opts.Publisher.PublishRaw(UsageSubject(r.opts.Instance), data)
}

func (r *Recorder) toOutbox(rec Record) {
	if err := r.opts.Store.PutOneWithTTL(OutboxPrefix+rec.ID, rec, r.opts.Retention); err != nil {
		r.logErr(err, "usage: store outbox record")
	}
}

// Start loads the totals from the stored records, sends a heartbeat, and
// then every HeartbeatInterval retries the outbox and sends a heartbeat.
// It returns at once; a second call does nothing.
func (r *Recorder) Start() {
	r.lifeMu.Lock()
	defer r.lifeMu.Unlock()
	if r.started || r.stopped {
		return
	}
	r.started = true
	r.startedAt = time.Now().UTC()
	r.loopDone = make(chan struct{})
	r.recount()
	r.heartbeat()
	go r.loop()
}

func (r *Recorder) loop() {
	defer close(r.loopDone)
	t := time.NewTicker(r.opts.HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			r.flushOutbox()
			r.recount()
			r.heartbeat()
		}
	}
}

// Close stops the heartbeat, publishes what is queued, and sends a final
// heartbeat when the Recorder was started. It is idempotent. Records
// arriving after Close are still stored, and wait in the outbox.
func (r *Recorder) Close() error {
	r.lifeMu.Lock()
	if r.stopped {
		r.lifeMu.Unlock()
		return nil
	}
	r.stopped = true
	started := r.started
	close(r.stop)
	r.lifeMu.Unlock()

	if started {
		<-r.loopDone
	}
	r.pubMu.Lock()
	r.closed = true
	close(r.pub)
	r.pubMu.Unlock()
	<-r.pubDone

	if started {
		r.recount()
		r.heartbeat()
	}
	return nil
}

// Totals returns the counters of the records the instance holds.
func (r *Recorder) Totals() Totals {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.totals
}

// recount rebuilds the totals from the stored records. Records expire by
// TTL after Retention, and only a scan sees that, so the totals are
// recomputed on every heartbeat instead of only incremented. One instance
// holds at most a retention window of runs, so a scan per heartbeat is
// cheap. It holds mu so a RecordRun is counted exactly once.
func (r *Recorder) recount() {
	r.mu.Lock()
	defer r.mu.Unlock()
	var recs []Record
	if err := r.opts.Store.Get(RecordPrefix, &recs); err != nil {
		r.logErr(err, "usage: load records")
		return
	}
	var t Totals
	for _, rec := range recs {
		t.Add(rec)
	}
	r.totals = t
}

// flushOutbox publishes up to OutboxFlushLimit outbox records, oldest
// first, deleting each one published, and stops at the first failure.
func (r *Recorder) flushOutbox() {
	if r.opts.Publisher == nil {
		return
	}
	var recs []Record
	if err := r.opts.Store.GetWithQuery(&store.Query{Prefix: OutboxPrefix, Limit: OutboxFlushLimit}, &recs); err != nil {
		r.logErr(err, "usage: read outbox")
		return
	}
	for _, rec := range recs {
		if err := r.publishRecord(rec); err != nil {
			r.logDebug(err, "usage: retry outbox")
			return
		}
		if err := r.opts.Store.DeleteOneByKey(OutboxPrefix + rec.ID); err != nil {
			r.logErr(err, "usage: delete outbox record")
		}
	}
}

func (r *Recorder) heartbeat() {
	h := Heartbeat{
		Instance:        r.opts.Instance,
		Service:         r.opts.Service,
		Host:            r.opts.Host,
		Version:         r.opts.Version,
		Provider:        r.opts.Provider,
		Model:           r.opts.Model,
		StartedAt:       r.startedAt,
		At:              time.Now().UTC(),
		IntervalSeconds: int(r.opts.HeartbeatInterval / time.Second),
		Totals:          r.Totals(),
	}
	if h.IntervalSeconds < 1 {
		h.IntervalSeconds = 1
	}
	if r.opts.Publisher != nil {
		data, err := json.Marshal(h)
		if err == nil {
			err = r.opts.Publisher.PublishRaw(HeartbeatSubject(r.opts.Instance), data)
		}
		if err != nil {
			r.logDebug(err, "usage: publish heartbeat")
		}
	}
	if r.opts.Local != nil {
		if err := r.opts.Local.PutHeartbeat(h); err != nil {
			r.logErr(err, "usage: local heartbeat")
		}
	}
}

// Records returns the stored records at or after since, newest first, at
// most limit of them (limit <= 0 returns all). A zero since returns every
// record still held.
func (r *Recorder) Records(ctx context.Context, since time.Time, limit int) ([]Record, error) {
	lower := RecordPrefix
	if !since.IsZero() {
		var id ulid.ULID
		if err := id.SetTime(ulid.Timestamp(since)); err == nil {
			lower = RecordPrefix + id.String()
		}
	}
	out := []Record{}
	err := r.opts.Store.DB.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Reverse = true
		opts.Prefix = []byte(RecordPrefix)
		it := txn.NewIterator(opts)
		defer it.Close()
		// "~" sorts after every ULID character.
		for it.Seek([]byte(RecordPrefix + "~")); it.Valid(); it.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			item := it.Item()
			if string(item.Key()) < lower {
				break
			}
			var rec Record
			if err := item.Value(func(v []byte) error { return json.Unmarshal(v, &rec) }); err != nil {
				return err
			}
			if !since.IsZero() && rec.At.Before(since) {
				continue
			}
			out = append(out, rec)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
		return nil
	})
	return out, err
}

func (r *Recorder) logErr(err error, msg string) {
	if r.opts.Logger != nil {
		r.opts.Logger.ErrorE().Err(err).Str("instance", r.opts.Instance).Msg(msg)
	}
}

func (r *Recorder) logDebug(err error, msg string) {
	if r.opts.Logger != nil {
		r.opts.Logger.DebugE().Err(err).Str("instance", r.opts.Instance).Msg(msg)
	}
}
