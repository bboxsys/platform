package main

import (
	"fmt"
	"time"

	"github.com/bboxsys/platform/admin"
	"github.com/bboxsys/platform/config"
	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/platform/queue"
	"github.com/bboxsys/platform/server"
)

// startQueue runs the job queue relay service in this process on
// queue_server.port. It
// connects to the controller as the "queue" service account, so producers
// and workers that should share its jobs connect as that account too. The
// admin server publishes it as queue_service and proxies /api/v1/queue to it
// for the Queue page. With queue_server.nats_api (default true) its API is
// also served over NATS as queue_server inside the queue account.
func startQueue(c *config.Config, l *logger.Logger, adminServer *admin.AdminServer, key, cert, ca []byte) *queue.QueueServer {
	sc, err := adminServer.ServiceCredentials("queue", "server")
	if err != nil {
		l.Fatal(fmt.Sprintf("queue: error creating service credentials: %v", err))
	}
	qs, err := queue.NewQueueServer(&server.ServerOptions{
		Application:        c.V.GetString("app.name"),
		Component:          "queue_server",
		PrivateKey:         key,
		Certificate:        cert,
		CA:                 ca,
		HTTPPort:           c.V.GetInt("queue_server.port"),
		Messenger:          true,
		MessengerServerURL: controllerURL(c),
		MessengerAccountID: sc.AccountID,
		MessengerJWT:       sc.JWT,
		MessengerSeed:      sc.Seed,
		TrustedAccounts:    sc.TrustedAccounts,
		NATSAPI:            boolOr(c, "queue_server.nats_api", true),
		NATSAPIDescription: "job queue relay API over NATS",
		Metadata: map[string]interface{}{
			"storeLocation": fmt.Sprintf("%s/%s", c.V.GetString("app.root_path"), "queue.db"),
		},
	})
	if err != nil {
		l.Fatal(fmt.Sprintf("queue: error creating queue server: %v", err))
	}
	go qs.Start()
	deadline := time.Now().Add(10 * time.Second)
	for !qs.Ready() {
		if time.Now().After(deadline) {
			l.Fatal("queue: server did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	l.InfoE().Int("port", c.V.GetInt("queue_server.port")).Msg("queue server ready")
	return qs
}
