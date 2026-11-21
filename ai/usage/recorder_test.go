package usage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bboxsys/platform/ai"
	"github.com/bboxsys/store"
	"github.com/dgraph-io/badger/v4"
)

// fakePublisher records what is published and fails while fail is set.
type fakePublisher struct {
	mu         sync.Mutex
	fail       bool
	records    []Record
	heartbeats []Heartbeat
}

func (f *fakePublisher) PublishRaw(subject string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("not connected")
	}
	switch {
	case strings.HasPrefix(subject, SubjectPrefix+".usage."):
		var r Record
		if err := json.Unmarshal(data, &r); err != nil {
			return err
		}
		f.records = append(f.records, r)
	case strings.HasPrefix(subject, SubjectPrefix+".heartbeat."):
		var h Heartbeat
		if err := json.Unmarshal(data, &h); err != nil {
			return err
		}
		f.heartbeats = append(f.heartbeats, h)
	default:
		return errors.New("unexpected subject " + subject)
	}
	return nil
}

func (f *fakePublisher) setFail(v bool) {
	f.mu.Lock()
	f.fail = v
	f.mu.Unlock()
}

func (f *fakePublisher) snapshot() ([]Record, []Heartbeat) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Record(nil), f.records...), append([]Heartbeat(nil), f.heartbeats...)
}

// fakeLocal is an in-process collector.
type fakeLocal struct {
	mu         sync.Mutex
	records    []Record
	heartbeats []Heartbeat
}

func (f *fakeLocal) PutRecord(r Record) error {
	f.mu.Lock()
	f.records = append(f.records, r)
	f.mu.Unlock()
	return nil
}

func (f *fakeLocal) PutHeartbeat(h Heartbeat) error {
	f.mu.Lock()
	f.heartbeats = append(f.heartbeats, h)
	f.mu.Unlock()
	return nil
}

func (f *fakeLocal) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.records), len(f.heartbeats)
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.NewStore(&store.Options{Location: t.TempDir(), Application: "test", Component: "usage"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func run(turns int, errMsg string) ai.RunRecord {
	return ai.RunRecord{
		AccountID: "ACCT", UserID: "U", SessionID: "S", Provider: "anthropic", Model: "m",
		Turns: turns, ToolCalls: turns - 1, Error: errMsg,
		Usage:   ai.Usage{InputTokens: int64(10 * turns), OutputTokens: 2},
		Started: time.Now(), Duration: time.Second,
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func outbox(t *testing.T, s *store.Store) []Record {
	t.Helper()
	var recs []Record
	if err := s.Get(OutboxPrefix, &recs); err != nil {
		t.Fatal(err)
	}
	return recs
}

func TestNewRecorderValidates(t *testing.T) {
	s := newStore(t)
	for _, name := range []string{"", "a.b", "a b", "a>"} {
		if _, err := NewRecorder(RecorderOptions{Instance: name, Store: s}); err == nil {
			t.Errorf("instance %q accepted", name)
		}
	}
	if _, err := NewRecorder(RecorderOptions{Instance: "x"}); err == nil {
		t.Error("missing store accepted")
	}
	r, err := NewRecorder(RecorderOptions{Instance: "admin_service-1", Store: s})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	o := r.Options()
	if o.Host == "" || o.Retention != DefaultRetention || o.HeartbeatInterval != DefaultHeartbeatInterval {
		t.Errorf("defaults = %+v", o)
	}
}

func TestRecorderPublishesAndReports(t *testing.T) {
	s := newStore(t)
	pub := &fakePublisher{}
	local := &fakeLocal{}
	r, err := NewRecorder(RecorderOptions{
		Instance: "svc", Service: "svc_service", Host: "h1", Version: "v1", Store: s,
		Publisher: pub, Local: local, HeartbeatInterval: 50 * time.Millisecond,
		Retention: time.Hour, Provider: "anthropic", Model: "m",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.Start()
	r.Start() // idempotent

	r.RecordRun(context.Background(), run(2, ""))
	waitFor(t, "published record", func() bool { recs, _ := pub.snapshot(); return len(recs) == 1 })
	recs, _ := pub.snapshot()
	first := recs[0]
	if first.ID == "" || first.Instance != "svc" || first.Service != "svc_service" || first.Host != "h1" || first.Turns != 2 || first.At.IsZero() {
		t.Errorf("published = %+v", first)
	}

	// Publishing fails: the record goes to the outbox and is published on
	// a later heartbeat with the same ID.
	pub.setFail(true)
	r.RecordRun(context.Background(), run(1, "boom"))
	waitFor(t, "outbox record", func() bool { return len(outbox(t, s)) == 1 })
	failed := outbox(t, s)[0]
	pub.setFail(false)
	waitFor(t, "outbox flush", func() bool {
		recs, _ := pub.snapshot()
		return len(recs) == 2 && len(outbox(t, s)) == 0
	})
	recs, _ = pub.snapshot()
	if recs[1].ID != failed.ID || recs[1].Error != "boom" {
		t.Errorf("retried = %+v, want ID %s", recs[1], failed.ID)
	}

	want := Totals{Runs: 2, Turns: 3, ToolCalls: 1, Errors: 1, Usage: ai.Usage{InputTokens: 30, OutputTokens: 4}}
	if got := r.Totals(); got != want {
		t.Errorf("totals = %+v, want %+v", got, want)
	}
	waitFor(t, "heartbeat with totals", func() bool {
		_, hbs := pub.snapshot()
		return len(hbs) > 0 && hbs[len(hbs)-1].Totals == want
	})
	_, hbs := pub.snapshot()
	hb := hbs[len(hbs)-1]
	if hb.Instance != "svc" || hb.Host != "h1" || hb.Version != "v1" || hb.Provider != "anthropic" || hb.Model != "m" || hb.StartedAt.IsZero() || hb.At.IsZero() || hb.IntervalSeconds != 1 {
		t.Errorf("heartbeat = %+v", hb)
	}
	nRecs, nHbs := local.counts()
	if nRecs != 2 || nHbs == 0 {
		t.Errorf("local got %d records, %d heartbeats", nRecs, nHbs)
	}

	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	_, before := pub.snapshot()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	_, after := pub.snapshot()
	if len(after) != len(before) {
		t.Error("second Close sent another heartbeat")
	}
	// A record after Close is still stored and waits in the outbox.
	r.RecordRun(context.Background(), run(1, ""))
	if len(outbox(t, s)) != 1 || r.Totals().Runs != 3 {
		t.Errorf("record after close: outbox %d, totals %+v", len(outbox(t, s)), r.Totals())
	}
}

func TestRecorderCloseSendsFinalHeartbeat(t *testing.T) {
	s := newStore(t)
	pub := &fakePublisher{}
	r, err := NewRecorder(RecorderOptions{Instance: "svc", Store: s, Publisher: pub, HeartbeatInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	r.Start()
	_, hbs := pub.snapshot()
	if len(hbs) != 1 || hbs[0].Totals.Runs != 0 {
		t.Fatalf("start heartbeats = %+v", hbs)
	}
	r.RecordRun(context.Background(), run(1, ""))
	r.Close()
	recs, hbs := pub.snapshot()
	if len(recs) != 1 {
		t.Errorf("Close did not drain the queue: %d records", len(recs))
	}
	if len(hbs) != 2 || hbs[1].Totals.Runs != 1 {
		t.Errorf("final heartbeat = %+v", hbs)
	}
}

func TestRecorderWithoutPublisherOrStart(t *testing.T) {
	s := newStore(t)
	local := &fakeLocal{}
	r, err := NewRecorder(RecorderOptions{Instance: "svc", Store: s, Local: local})
	if err != nil {
		t.Fatal(err)
	}
	r.RecordRun(context.Background(), run(1, ""))
	// Local already has the record, so nothing waits for a publisher.
	if n := len(outbox(t, s)); n != 0 {
		t.Errorf("outbox with a Local = %d, want 0", n)
	}
	got, err := r.Records(context.Background(), time.Time{}, 0)
	if err != nil || len(got) != 1 {
		t.Fatalf("records = %v %v", got, err)
	}
	if n, _ := local.counts(); n != 1 || r.Totals().Runs != 1 {
		t.Errorf("local %d, totals %+v", n, r.Totals())
	}
	r.Close()
	if _, hbs := local.counts(); hbs != 0 {
		t.Errorf("unstarted Close sent %d heartbeats", hbs)
	}

	// A new Recorder on the same store loads the totals on Start.
	r2, err := NewRecorder(RecorderOptions{Instance: "svc", Store: s, Local: local, HeartbeatInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	r2.Start()
	if r2.Totals().Runs != 1 {
		t.Errorf("loaded totals = %+v", r2.Totals())
	}
}

func TestRecorderRecordsOrderAndRetention(t *testing.T) {
	s := newStore(t)
	r, err := NewRecorder(RecorderOptions{Instance: "svc", Store: s, Retention: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var mid time.Time
	for i := 1; i <= 5; i++ {
		if i == 4 {
			time.Sleep(5 * time.Millisecond)
			mid = time.Now()
			time.Sleep(5 * time.Millisecond)
		}
		r.RecordRun(context.Background(), run(i, ""))
		time.Sleep(2 * time.Millisecond)
	}
	all, err := r.Records(context.Background(), time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 {
		t.Fatalf("records = %d", len(all))
	}
	for i, rec := range all {
		if rec.Turns != 5-i {
			t.Errorf("record %d has turns %d: not newest first", i, rec.Turns)
		}
	}
	limited, _ := r.Records(context.Background(), time.Time{}, 2)
	if len(limited) != 2 || limited[0].Turns != 5 || limited[1].Turns != 4 {
		t.Errorf("limited = %+v", limited)
	}
	since, _ := r.Records(context.Background(), mid, 0)
	if len(since) != 2 || since[0].Turns != 5 || since[1].Turns != 4 {
		t.Errorf("since = %d records", len(since))
	}

	// Every record and outbox entry carries the retention TTL.
	checkTTL(t, s, RecordPrefix+all[0].ID, time.Hour)
	checkTTL(t, s, OutboxPrefix+all[0].ID, time.Hour)
}

// checkTTL asserts that key expires about ttl from now.
func checkTTL(t *testing.T, s *store.Store, key string, ttl time.Duration) {
	t.Helper()
	err := s.DB.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}
		left := time.Until(time.Unix(int64(item.ExpiresAt()), 0))
		if item.ExpiresAt() == 0 || left > ttl || left < ttl-time.Minute {
			t.Errorf("%s expires in %v, want about %v", key, left, ttl)
		}
		return nil
	})
	if err != nil {
		t.Errorf("%s: %v", key, err)
	}
}

// A host that is its own collector (Local, no Publisher) keeps no outbox.
func TestRecorderLocalOnlyKeepsNoOutbox(t *testing.T) {
	st := newStore(t)
	local := &fakeLocal{}
	r, err := NewRecorder(RecorderOptions{Instance: "admin_service", Store: st, Local: local})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.RecordRun(context.Background(), ai.RunRecord{Model: "m", Turns: 1})
	var out []Record
	if err := st.Get(OutboxPrefix, &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("outbox = %d records, want 0", len(out))
	}
	if n, _ := local.counts(); n != 1 {
		t.Fatalf("local records = %d, want 1", n)
	}
}

// With neither a Publisher nor a Local, records wait in the outbox for a
// restart with a publisher.
func TestRecorderNoSinkUsesOutbox(t *testing.T) {
	s := newStore(t)
	r, err := NewRecorder(RecorderOptions{Instance: "svc", Store: s})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.RecordRun(context.Background(), run(1, ""))
	if n := len(outbox(t, s)); n != 1 {
		t.Errorf("outbox = %d, want 1", n)
	}
}
