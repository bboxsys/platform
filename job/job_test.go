package job_test

import (
	"errors"
	"testing"
	"time"

	"github.com/bboxsys/platform/job"
	"github.com/bboxsys/platform/messenger"
)

func TestSubjects(t *testing.T) {
	j := job.New("media", "thumbnail", nil)
	if j.ID == "" {
		t.Fatal("New must assign an ID")
	}
	if got, want := j.RequestSubject(), "job.request.media.thumbnail"; got != want {
		t.Errorf("RequestSubject = %q, want %q", got, want)
	}
	if got, want := j.QueueSubject(), "job.queue.media.thumbnail"; got != want {
		t.Errorf("QueueSubject = %q, want %q", got, want)
	}
	if got, want := j.StatusSubject(), "job.status."+j.ID; got != want {
		t.Errorf("StatusSubject = %q, want %q", got, want)
	}
	if job.New("a", "b", nil).ID == j.ID {
		t.Error("IDs must be unique")
	}
}

func TestNewStatusFlattensErrors(t *testing.T) {
	j := job.New("media", "thumbnail", nil)
	s := job.NewStatus(j, &job.StatusOptions{State: job.StateError, Errors: []error{errors.New("boom"), nil}})
	if s.JobID != j.ID || s.Service != "media" || s.JobName != "thumbnail" {
		t.Errorf("status does not carry job identity: %+v", s)
	}
	if len(s.Errors) != 1 || s.Errors[0] != "boom" {
		t.Errorf("Errors = %v, want [boom]", s.Errors)
	}
	if s.Timestamp.IsZero() {
		t.Error("Timestamp not set")
	}
	if job.NewStatus(j, nil).State != "" {
		t.Error("nil options must produce an empty status")
	}
}

func TestNotConnected(t *testing.T) {
	j := job.New("media", "thumbnail", nil)
	for name, err := range map[string]error{
		"nil client":   j.Dispatch(nil),
		"empty client": j.Enqueue(&messenger.Client{}),
		"status":       j.DispatchStatus(nil, nil),
	} {
		if !errors.Is(err, job.ErrNotConnected) {
			t.Errorf("%s: err = %v, want ErrNotConnected", name, err)
		}
	}
	if _, err := j.SubscribeStatus(nil, nil); !errors.Is(err, job.ErrNotConnected) {
		t.Errorf("SubscribeStatus err = %v", err)
	}
	if _, err := job.SubscribeRequests(nil, nil); !errors.Is(err, job.ErrNotConnected) {
		t.Errorf("SubscribeRequests err = %v", err)
	}
	if _, err := job.SubscribeStatuses(nil, nil); !errors.Is(err, job.ErrNotConnected) {
		t.Errorf("SubscribeStatuses err = %v", err)
	}
}

type args struct {
	ItemID string `json:"item_id"`
	Size   int    `json:"size"`
}

func TestDispatchStatusRoundTrip(t *testing.T) {
	mc := newTestClient(t)

	requests := make(chan *job.Job, 1)
	if _, err := job.SubscribeRequests(mc, func(j *job.Job) { requests <- j }); err != nil {
		t.Fatal(err)
	}
	all := make(chan *job.Status, 4)
	if _, err := job.SubscribeStatuses(mc, func(s *job.Status) { all <- s }); err != nil {
		t.Fatal(err)
	}

	j := job.New("media", "thumbnail", args{ItemID: "4858484", Size: 256})
	mine := make(chan *job.Status, 4)
	if _, err := j.SubscribeStatus(mc, func(s *job.Status) { mine <- s }); err != nil {
		t.Fatal(err)
	}
	if err := j.Dispatch(mc); err != nil {
		t.Fatal(err)
	}

	var got *job.Job
	select {
	case got = <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("request not delivered")
	}
	if got.ID != j.ID || got.Service != "media" || got.Name != "thumbnail" {
		t.Fatalf("delivered job = %+v", got)
	}
	var a args
	if err := got.DecodePayload(&a); err != nil {
		t.Fatal(err)
	}
	if a.ItemID != "4858484" || a.Size != 256 {
		t.Errorf("payload = %+v", a)
	}

	if err := got.DispatchStatus(mc, &job.StatusOptions{State: job.StateComplete, Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	for name, ch := range map[string]chan *job.Status{"per-job": mine, "wildcard": all} {
		select {
		case s := <-ch:
			if s.JobID != j.ID || s.State != job.StateComplete || s.Result != "ok" {
				t.Errorf("%s status = %+v", name, s)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s status not delivered", name)
		}
	}
}
