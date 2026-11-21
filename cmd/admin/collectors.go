package main

import (
	"time"

	"github.com/bboxsys/platform/admin"
	"github.com/bboxsys/platform/config"
	"github.com/bboxsys/platform/controller"
	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/platform/messenger"
	"github.com/bboxsys/platform/queue"
	"github.com/bboxsys/platform/storemetrics"
)

// logPublishMaxPerSec caps how many log events per second each logger
// publishes to NATS.
const logPublishMaxPerSec = 100

// collectors owns the SYS-account publishers that feed this process's own
// log events and store metrics into the admin stores. The subscriptions
// that receive them (one per account) are admin.Collectors, started by
// StartCollectors and closed with the admin server. Without both, the log
// and metrics stores are opened but never written to.
type collectors struct {
	clients    []*messenger.Client
	publishers []*storemetrics.Publisher
}

// startCollectors starts the admin package's per-account log and metrics
// collectors, attaches a NATS publisher to this process's loggers so its
// own events reach the log store, and starts a periodic metrics publisher
// for the admin's stores. The controller must be reachable.
func startCollectors(c *config.Config, l *logger.Logger, adminServer *admin.AdminServer, ctrl *controller.Controller, qs *queue.QueueServer, key, cert, ca []byte) *collectors {
	col := &collectors{}
	if _, err := adminServer.StartCollectors(admin.CollectorOptions{
		Application:   c.V.GetString("app.name"),
		ControllerURL: controllerURL(c),
		PrivateKey:    key,
		Certificate:   cert,
		CA:            ca,
	}); err != nil {
		l.ErrorE().Err(err).Msg("collectors: start failed")
	}

	sysClient := func(name string) *messenger.Client {
		sc, err := adminServer.ServiceCredentials("SYS", name)
		if err != nil {
			l.ErrorE().Str("service", name).Err(err).Msg("collectors: SYS credentials failed")
			return nil
		}
		mc, err := messenger.NewClient(&messenger.ClientOptions{
			Application:   c.V.GetString("app.name"),
			Component:     name,
			ControllerURL: controllerURL(c),
			AccountID:     sc.AccountID,
			PrivateKey:    key,
			Certificate:   cert,
			CA:            ca,
			JWT:           sc.JWT,
			Seed:          sc.Seed,
		})
		if err != nil {
			l.ErrorE().Str("service", name).Err(err).Msg("collectors: SYS messenger connect failed")
			return nil
		}
		col.clients = append(col.clients, mc)
		return mc
	}

	// Publish this process's own loggers. The command logger and the admin
	// server share the admin_server name because they are one service
	// here; the embedded controller reports as controller.
	if adminServer.LogStore() != nil {
		if mc := sysClient("admin_server_log"); mc != nil {
			adminServer.Logger.AttachPublisher(mc, "admin_server", logPublishMaxPerSec)
			l.AttachPublisher(mc, "admin_server", logPublishMaxPerSec)
		}
		if ctrl != nil {
			if mc := sysClient("controller_log"); mc != nil {
				ctrl.Logger.AttachPublisher(mc, "controller", logPublishMaxPerSec)
			}
		}
		if qs != nil {
			if mc := sysClient("queue_server_log"); mc != nil {
				qs.Logger.AttachPublisher(mc, "queue_server", logPublishMaxPerSec)
			}
		}
	}

	// Periodic metrics for the admin's own stores.
	if ms := adminServer.MetricsStore(); ms != nil {
		stores := []storemetrics.StoreInfo{}
		if base := adminServer.GetBaseStore(); base != nil {
			stores = append(stores, storemetrics.StoreInfo{Name: "admin.db", Store: base})
		}
		if ls := adminServer.LogStore(); ls != nil {
			stores = append(stores, storemetrics.StoreInfo{Name: "logs.db", Store: ls.Store})
		}
		stores = append(stores, storemetrics.StoreInfo{Name: "metrics.db", Store: ms.Store})
		if us := adminServer.AIUsageStore(); us != nil {
			stores = append(stores, storemetrics.StoreInfo{Name: "ai_usage.db", Store: us.Store})
		}
		// The assistant's own store ({root}/ai/ai.db); startAI runs before
		// the collectors, so it is open here when the assistant is enabled.
		if st := adminServer.AIStore(); st != nil {
			stores = append(stores, storemetrics.StoreInfo{Name: "ai/ai.db", Store: st})
		}
		if qs != nil {
			if base := qs.GetBaseStore(); base != nil {
				stores = append(stores, storemetrics.StoreInfo{Name: "queue.db", Store: base})
			}
		}
		if mc := sysClient("admin_server_metrics"); mc != nil {
			interval := time.Duration(positive(c.V.GetInt("metrics.collect_interval_seconds"), 30)) * time.Second
			pub := storemetrics.NewPublisher("admin_server", stores, interval, mc, l)
			pub.Start()
			col.publishers = append(col.publishers, pub)
			l.InfoE().Int("stores", len(stores)).Dur("interval", interval).Msg("metrics publisher started")
		}
	}

	return col
}

// Close stops the publishers and closes every NATS client.
func (col *collectors) Close() {
	for _, pub := range col.publishers {
		pub.Stop()
	}
	for _, mc := range col.clients {
		mc.Close()
	}
}
