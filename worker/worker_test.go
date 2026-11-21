package worker_test

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bboxsys/platform/job"
	"github.com/bboxsys/platform/messenger"
	"github.com/bboxsys/platform/worker"
	natsd "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

func newTestClient(t *testing.T) *messenger.Client {
	t.Helper()
	srv, err := natsd.NewServer(&natsd.Options{Host: "127.0.0.1", Port: -1, NoSigs: true, NoLog: true})
	if err != nil {
		t.Fatalf("nats server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats server did not start")
	}
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	mc, err := messenger.NewClientFromConn(nc, "platform", "worker_test")
	if err != nil {
		t.Fatalf("messenger client: %v", err)
	}
	t.Cleanup(mc.Close)
	return mc
}

func collect(t *testing.T, ch <-chan *job.Status, n int) []*job.Status {
	t.Helper()
	var out []*job.Status
	for len(out) < n {
		select {
		case s := <-ch:
			out = append(out, s)
		case <-time.After(5 * time.Second):
			t.Fatalf("got %d of %d statuses: %+v", len(out), n, out)
		}
	}
	return out
}

func TestNewDefaults(t *testing.T) {
	w := worker.New(&worker.Options{Service: "media", JobName: "thumbnail"})
	if got, want := w.Subject(), "job.queue.media.thumbnail"; got != want {
		t.Errorf("Subject = %q, want %q", got, want)
	}
	if _, err := w.Subscribe(func(*job.Job) {}); !errors.Is(err, job.ErrNotConnected) {
		t.Errorf("Subscribe without client: err = %v", err)
	}
}

func TestRunReportsLifecycle(t *testing.T) {
	mc := newTestClient(t)
	j := job.New("media", "thumbnail", map[string]string{"item_id": "1"})
	statuses := make(chan *job.Status, 8)
	if _, err := j.SubscribeStatus(mc, func(s *job.Status) { statuses <- s }); err != nil {
		t.Fatal(err)
	}

	w := worker.New(&worker.Options{Service: "media", JobName: "thumbnail", MC: mc})
	if _, err := w.Run(func(got *job.Job) (string, error) {
		var p map[string]string
		if err := got.DecodePayload(&p); err != nil {
			return "", err
		}
		return "rendered " + p["item_id"], nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := j.Enqueue(mc); err != nil {
		t.Fatal(err)
	}

	got := collect(t, statuses, 2)
	if got[0].State != job.StateStarted || !got[0].Running {
		t.Errorf("first status = %+v, want started/running", got[0])
	}
	if got[1].State != job.StateComplete || got[1].Result != "rendered 1" || got[1].Running {
		t.Errorf("second status = %+v, want complete with result", got[1])
	}
}

func TestRunReportsErrorsAndPanics(t *testing.T) {
	mc := newTestClient(t)
	w := worker.New(&worker.Options{Service: "media", JobName: "thumbnail", MC: mc})
	if _, err := w.Run(func(got *job.Job) (string, error) {
		switch got.Payload {
		case "panic":
			panic("bad input")
		default:
			return "", errors.New("render failed")
		}
	}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ payload, want string }{
		{"error", "render failed"},
		{"panic", "worker: handler panic: bad input"},
	} {
		j := job.New("media", "thumbnail", tc.payload)
		statuses := make(chan *job.Status, 8)
		sub, err := j.SubscribeStatus(mc, func(s *job.Status) { statuses <- s })
		if err != nil {
			t.Fatal(err)
		}
		if err := j.Enqueue(mc); err != nil {
			t.Fatal(err)
		}
		got := collect(t, statuses, 2)
		if got[1].State != job.StateError || len(got[1].Errors) != 1 || got[1].Errors[0] != tc.want {
			t.Errorf("%s: final status = %+v, want error %q", tc.payload, got[1], tc.want)
		}
		sub.Unsubscribe()
	}
}

func TestQueueGroupDeliversOnce(t *testing.T) {
	mc := newTestClient(t)
	var handled int32
	done := make(chan struct{}, 8)
	for i := 0; i < 3; i++ {
		w := worker.New(&worker.Options{Service: "media", JobName: "thumbnail", MC: mc})
		if _, err := w.Subscribe(func(*job.Job) {
			atomic.AddInt32(&handled, 1)
			done <- struct{}{}
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := job.New("media", "thumbnail", nil).Enqueue(mc); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("job not delivered")
	}
	time.Sleep(200 * time.Millisecond)
	if n := atomic.LoadInt32(&handled); n != 1 {
		t.Errorf("job handled %d times, want exactly 1", n)
	}
}
