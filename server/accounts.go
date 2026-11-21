package server

import (
	"errors"
	"fmt"
	"sync"

	client "github.com/bboxsys/platform/admin/client"
	"github.com/bboxsys/platform/messenger"
	"github.com/nats-io/nats.go"
)

// AccountRef names a tenant account to follow.
type AccountRef struct {
	ID   string
	Name string
}

// AccountCredentialsProvider mints the NATS credentials a service uses to
// connect inside a tenant account. The admin server implements it for the
// process that hosts it; a remote service can implement it over
// GET /api/v1/servicecredentials/{accountName}/{serviceName}.
type AccountCredentialsProvider interface {
	ServiceCredentials(accountID, accountName string) (jwt, seed string, err error)
}

// AccountConnector opens a NATS connection inside an account with the
// given credentials and returns it together with a function that closes
// it. The default connector uses the server's TLS material and
// MessengerServerURL through package messenger; tests inject a plain one.
type AccountConnector func(accountID, jwt, seed string) (nc *nats.Conn, closeFn func(), err error)

// FollowOption configures FollowAccounts.
type FollowOption func(*AccountConnections)

// WithAccountConnector replaces the default connector.
func WithAccountConnector(c AccountConnector) FollowOption {
	return func(ac *AccountConnections) { ac.connector = c }
}

// AccountConnections serves the NATS API inside every tenant account. NATS
// accounts isolate subjects, so a service connected in its own account is
// invisible to a caller inside a tenant account; the follower therefore
// opens one connection per account, serves the API on each, and follows
// accounts as the admin creates and deletes them (tenant.notify.*).
type AccountConnections struct {
	server    *Server
	provider  AccountCredentialsProvider
	connector AccountConnector

	mu     sync.Mutex
	conns  map[string]func()
	subs   []*nats.Subscription
	closed bool
}

// FollowAccounts serves the NATS API inside each of accounts (except the
// server's own messenger account, which StartNATSAPI serves) and keeps
// following tenant lifecycle notifications on the primary messenger
// connection. The server must have a messenger client.
func (server *Server) FollowAccounts(provider AccountCredentialsProvider, accounts []AccountRef, opts ...FollowOption) (*AccountConnections, error) {
	if provider == nil {
		return nil, errors.New("follow accounts: provider is required")
	}
	if server.MessengerClient == nil || server.MessengerClient.Client == nil {
		return nil, errors.New("follow accounts: no messenger client")
	}
	ac := &AccountConnections{
		server:   server,
		provider: provider,
		conns:    make(map[string]func()),
	}
	ac.connector = server.defaultAccountConnector()
	for _, o := range opts {
		o(ac)
	}
	for _, a := range accounts {
		ac.AddAccount(a.ID, a.Name)
	}
	ec := server.MessengerClient.Client
	created, err := ec.Subscribe(client.SubjectTenantCreated, func(n *client.TenantCreatedNotification) {
		if n != nil {
			ac.AddAccount(n.AccountID, n.AccountName)
		}
	})
	if err != nil {
		ac.Close()
		return nil, fmt.Errorf("follow accounts: subscribe created: %w", err)
	}
	ac.subs = append(ac.subs, created)
	deleted, err := ec.Subscribe(client.SubjectTenantDeleted, func(n *client.TenantDeletedNotification) {
		if n != nil {
			ac.RemoveAccount(n.AccountID)
		}
	})
	if err != nil {
		ac.Close()
		return nil, fmt.Errorf("follow accounts: subscribe deleted: %w", err)
	}
	ac.subs = append(ac.subs, deleted)
	return ac, nil
}

func (server *Server) primaryAccountID() string {
	if id := server.ServerOptions.MessengerAccountID; id != "" {
		return id
	}
	if server.MessengerClient != nil {
		return server.MessengerClient.AccountID
	}
	return ""
}

func (server *Server) defaultAccountConnector() AccountConnector {
	o := server.ServerOptions
	return func(accountID, jwt, seed string) (*nats.Conn, func(), error) {
		mc, err := messenger.NewClient(&messenger.ClientOptions{
			Application:   o.Component,
			Component:     "nats_api",
			ControllerURL: o.MessengerServerURL,
			AccountID:     accountID,
			PrivateKey:    o.PrivateKey,
			Certificate:   o.Certificate,
			CA:            o.CA,
			JWT:           jwt,
			Seed:          seed,
		})
		if err != nil {
			return nil, nil, err
		}
		return mc.Client.Conn, mc.Close, nil
	}
}

// AddAccount connects inside the account and serves the API there. It is
// idempotent per account ID, skips the primary account, and is a no-op
// after Close. Failures are logged, not returned, because the follower
// runs from notifications.
func (ac *AccountConnections) AddAccount(accountID, accountName string) {
	if accountID == "" || accountID == ac.server.primaryAccountID() {
		return
	}
	ac.mu.Lock()
	defer ac.mu.Unlock()
	if ac.closed {
		return
	}
	if _, ok := ac.conns[accountID]; ok {
		return
	}
	l := ac.server.Logger
	jwt, seed, err := ac.provider.ServiceCredentials(accountID, accountName)
	if err != nil {
		l.ErrorE().Str("accountID", accountID).Str("account", accountName).Err(err).Msg("nats api: account credentials failed")
		return
	}
	nc, closeFn, err := ac.connector(accountID, jwt, seed)
	if err != nil {
		l.ErrorE().Str("accountID", accountID).Str("account", accountName).Err(err).Msg("nats api: account connect failed")
		return
	}
	if err := ac.server.ServeNATSAccount(accountID, nc); err != nil {
		l.ErrorE().Str("accountID", accountID).Str("account", accountName).Err(err).Msg("nats api: account serve failed")
		closeFn()
		return
	}
	ac.conns[accountID] = closeFn
	l.InfoE().Str("accountID", accountID).Str("account", accountName).Msg("nats api: serving account")
}

// RemoveAccount stops serving the account and closes its connection.
func (ac *AccountConnections) RemoveAccount(accountID string) {
	ac.mu.Lock()
	closeFn, ok := ac.conns[accountID]
	delete(ac.conns, accountID)
	ac.mu.Unlock()
	if !ok {
		return
	}
	_ = ac.server.StopNATSAccount(accountID)
	closeFn()
}

// Accounts returns the IDs of the followed accounts currently served.
func (ac *AccountConnections) Accounts() []string {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	ids := make([]string, 0, len(ac.conns))
	for id := range ac.conns {
		ids = append(ids, id)
	}
	return ids
}

// Close drops the notification subscriptions and closes every account
// connection. The primary connection is left alone.
func (ac *AccountConnections) Close() {
	ac.mu.Lock()
	ac.closed = true
	subs := ac.subs
	ac.subs = nil
	conns := ac.conns
	ac.conns = make(map[string]func())
	ac.mu.Unlock()
	for _, s := range subs {
		_ = s.Unsubscribe()
	}
	for id, closeFn := range conns {
		_ = ac.server.StopNATSAccount(id)
		closeFn()
	}
}
