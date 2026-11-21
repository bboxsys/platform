package admin

import (
	"errors"

	"github.com/bboxsys/platform/natsapi"
)

// AdminNATSService is the subject token and micro service name the admin
// API is served under: api.admin_service.{METHOD}.{resource}.
const AdminNATSService = "admin_service"

// adminNATSAPI wraps the bridge so the AdminServer struct needs no import.
type adminNATSAPI struct {
	*natsapi.Service
}

// NATSAPIOptions configures StartNATSAPI.
type NATSAPIOptions struct {
	// Version is the micro service version (semver). Default 1.0.0.
	Version string
	// Description is published in the micro service info.
	Description string
}

// StartNATSAPI serves the admin HTTP API over NATS (package natsapi) as
// admin_service, with the full handler chain, so every route keeps its
// REST auth tier. NATS accounts isolate subjects, so the API is served on
// the per-account connections the collectors own (StartCollectors): every
// account in the store, followed as accounts are created and deleted. It
// works whether it is called before or after StartCollectors, and is
// idempotent. The admin's primary messenger connection is not used: its
// account is one of the collected accounts.
func (adminServer *AdminServer) StartNATSAPI(opts NATSAPIOptions) error {
	if adminServer.Srv == nil || adminServer.Srv.Handler == nil {
		return errors.New("nats api: admin server has no handler")
	}
	adminServer.natsAPIMu.Lock()
	if adminServer.natsAPI == nil {
		svc, err := natsapi.New(natsapi.Options{
			Service:     AdminNATSService,
			Handler:     adminServer.Srv.Handler,
			Version:     opts.Version,
			Description: opts.Description,
			Logger:      adminServer.Logger,
		})
		if err != nil {
			adminServer.natsAPIMu.Unlock()
			return err
		}
		adminServer.natsAPI = &adminNATSAPI{Service: svc}
	}
	api := adminServer.natsAPI
	adminServer.natsAPIMu.Unlock()

	// Serve the collector connections that are already open.
	if col := adminServer.collectors; col != nil {
		col.mu.Lock()
		for accountID, mc := range col.clients {
			if mc.Client == nil || mc.Client.Conn == nil {
				continue
			}
			if err := api.Serve(accountID, mc.Client.Conn); err != nil {
				adminServer.Logger.ErrorE().Str("accountID", accountID).Err(err).Msg("nats api: serve failed")
			}
		}
		col.mu.Unlock()
	}
	adminServer.Logger.Info("nats api: admin_service started")
	return nil
}

// NATSAPI returns the running bridge, or nil when StartNATSAPI has not
// been called.
func (adminServer *AdminServer) NATSAPI() *natsapi.Service {
	adminServer.natsAPIMu.Lock()
	defer adminServer.natsAPIMu.Unlock()
	if adminServer.natsAPI == nil {
		return nil
	}
	return adminServer.natsAPI.Service
}

func (adminServer *AdminServer) closeNATSAPI() {
	adminServer.natsAPIMu.Lock()
	api := adminServer.natsAPI
	adminServer.natsAPI = nil
	adminServer.natsAPIMu.Unlock()
	if api != nil {
		api.Close()
	}
}
