package admin

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/bboxsys/platform/ai/usage"
	"github.com/bboxsys/platform/logentry"
	"github.com/bboxsys/platform/messenger"
	"github.com/bboxsys/platform/storemetrics"
	"github.com/nats-io/nats.go"
)

// collectorService is the per-account service user the collectors connect as.
const collectorService = "log_collector"

// CollectorOptions configures the NATS connections the collectors open.
type CollectorOptions struct {
	// Application names the NATS connections ({Application}_log_collector).
	Application string
	// ControllerURL is the NATS URL of the controller.
	ControllerURL string
	// TLS client credentials the controller verifies, normally the admin
	// server's own certificate and the CA it trusts.
	PrivateKey  []byte
	Certificate []byte
	CA          []byte
}

// Collectors pull every service's log and metrics events into the admin
// server's log and metrics stores. NATS accounts isolate subjects, so a
// publisher inside a tenant account (a host agent, a tenant-scoped service)
// is invisible to a subscriber in SYS. The collectors therefore subscribe
// once per account, as that account's own log_collector service user, and
// follow accounts as they are created and deleted.
type Collectors struct {
	adminServer *AdminServer
	opts        CollectorOptions

	mu      sync.Mutex
	clients map[string]*messenger.Client // by account ID
	closed  bool
}

// StartCollectors subscribes to the log and metrics subjects in every
// account known to the store and registers the collectors with the admin
// server so new and deleted accounts are followed. At least one of the log,
// metrics, and AI usage stores must be initialized, or the NATS API must
// have been started (StartNATSAPI): the per-account connections also serve
// it.
func (adminServer *AdminServer) StartCollectors(opts CollectorOptions) (*Collectors, error) {
	if adminServer.logStore == nil && adminServer.metricsStore == nil && adminServer.aiUsageStore == nil && adminServer.NATSAPI() == nil {
		return nil, errors.New("collectors: no log, metrics, or ai usage store initialized and no nats api")
	}
	accounts, err := adminServer.st.GetAccounts()
	if err != nil {
		return nil, err
	}
	col := &Collectors{
		adminServer: adminServer,
		opts:        opts,
		clients:     make(map[string]*messenger.Client),
	}
	for _, account := range *accounts {
		col.AddAccount(account.ID, account.Name)
	}
	adminServer.collectors = col
	return col, nil
}

// Collectors returns the running collectors, or nil when none were started.
func (adminServer *AdminServer) Collectors() *Collectors {
	return adminServer.collectors
}

// AddAccount opens a collector connection inside the given account. It is
// idempotent per account ID and a no-op after Close.
func (col *Collectors) AddAccount(accountID, accountName string) {
	col.mu.Lock()
	defer col.mu.Unlock()
	if col.closed {
		return
	}
	if _, ok := col.clients[accountID]; ok {
		return
	}
	l := col.adminServer.Logger
	sc, err := col.adminServer.serviceCredentialsForAccount(accountID, collectorService)
	if err != nil {
		l.ErrorE().Str("accountID", accountID).Str("account", accountName).Err(err).Msg("collectors: service credentials failed")
		return
	}
	mc, err := messenger.NewClient(&messenger.ClientOptions{
		Application:   col.opts.Application,
		Component:     collectorService,
		ControllerURL: col.opts.ControllerURL,
		AccountID:     accountID,
		PrivateKey:    col.opts.PrivateKey,
		Certificate:   col.opts.Certificate,
		CA:            col.opts.CA,
		JWT:           sc.JWT,
		Seed:          sc.Seed,
	})
	if err != nil {
		l.ErrorE().Str("accountID", accountID).Str("account", accountName).Err(err).Msg("collectors: connect failed")
		return
	}
	if ls := col.adminServer.logStore; ls != nil {
		mc.SubscribeRaw(logentry.NATSSubjectWildcard, func(data []byte) {
			var entry logentry.LogEntry
			if err := json.Unmarshal(data, &entry); err != nil {
				return
			}
			// Errors are dropped on purpose: logging them would recurse.
			_ = ls.PutLogEntry(&entry)
		})
	}
	if ms := col.adminServer.metricsStore; ms != nil {
		mc.SubscribeRaw(storemetrics.NATSSubjectWildcard, func(data []byte) {
			var entry storemetrics.StoreMetricsEntry
			if err := json.Unmarshal(data, &entry); err != nil {
				return
			}
			_ = ms.PutMetricsEntry(&entry)
		})
	}
	if us := col.adminServer.aiUsageStore; us != nil {
		if err := subscribeAIUsage(mc.Client.Conn, accountID, us); err != nil {
			l.ErrorE().Str("accountID", accountID).Str("account", accountName).Err(err).Msg("collectors: ai usage subscribe failed")
		}
	}
	if api := col.adminServer.NATSAPI(); api != nil {
		if err := api.Serve(accountID, mc.Client.Conn); err != nil {
			l.ErrorE().Str("accountID", accountID).Str("account", accountName).Err(err).Msg("collectors: nats api serve failed")
		}
	}
	col.clients[accountID] = mc
	l.InfoE().Str("accountID", accountID).Str("account", accountName).Msg("collectors: subscribed to account logs and metrics")
}

// RemoveAccount closes the collector connection for a deleted account.
func (col *Collectors) RemoveAccount(accountID string) {
	col.mu.Lock()
	defer col.mu.Unlock()
	if mc, ok := col.clients[accountID]; ok {
		if api := col.adminServer.NATSAPI(); api != nil {
			_ = api.Stop(accountID)
		}
		mc.Close()
		delete(col.clients, accountID)
	}
}

// Conn returns the collector connection inside an account, or nil when
// the account is not collected. The connection stays the collectors' own.
func (col *Collectors) Conn(accountID string) *nats.Conn {
	col.mu.Lock()
	defer col.mu.Unlock()
	if mc, ok := col.clients[accountID]; ok && mc.Client != nil {
		return mc.Client.Conn
	}
	return nil
}

// Accounts returns the IDs of the accounts currently being collected.
func (col *Collectors) Accounts() []string {
	col.mu.Lock()
	defer col.mu.Unlock()
	ids := make([]string, 0, len(col.clients))
	for id := range col.clients {
		ids = append(ids, id)
	}
	return ids
}

// Close closes every collector connection.
func (col *Collectors) Close() {
	col.mu.Lock()
	defer col.mu.Unlock()
	col.closed = true
	for id, mc := range col.clients {
		mc.Close()
		delete(col.clients, id)
	}
}

// subscribeAIUsage stores every AI usage record and heartbeat published in
// the account nc belongs to. Malformed messages are dropped silently:
// logging them would recurse into the log collector.
func subscribeAIUsage(nc *nats.Conn, accountID string, us *AIUsageStore) error {
	_, err := nc.Subscribe(usage.SubjectWildcard, func(msg *nats.Msg) {
		_ = us.handleAIUsageMessage(accountID, msg.Subject, msg.Data)
	})
	return err
}
