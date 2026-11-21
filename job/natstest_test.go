package job_test

import (
	"testing"
	"time"

	"github.com/bboxsys/platform/messenger"
	natsd "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// newTestClient starts an embedded NATS server on a random port and returns
// a messenger client bound to it. Both are torn down with the test.
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
	mc, err := messenger.NewClientFromConn(nc, "platform", "job_test")
	if err != nil {
		t.Fatalf("messenger client: %v", err)
	}
	t.Cleanup(mc.Close)
	return mc
}
