// /////////////////////////////////////////////////////////////////////
// David Gurley
// dmgurley@gmail.com
// May 2019
//
// //////////////////////////////////////////////////////////////////////
package server

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bboxsys/platform/clientip"
	"github.com/bboxsys/platform/letsencrypt"
	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/platform/messenger"
	"github.com/bboxsys/platform/natsapi"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/nats-io/nats.go"
)

const (
	EndpointNotImplementedResponse = `{"status": "404", "message": "endpoint not implemented"}`
	NoopResponse                   = `{"status": "200", "message": "noop"}`
	Unauthorized                   = `{"status": "401", "message": "unauthorized"}`
	InternalServerError            = `{"status": "500", "message": "internal server error"}`
)

// tlsErrorFilter filters out common TLS handshake errors caused by health checkers
// doing TCP-only connections to TLS endpoints. These are cosmetic errors.
type tlsErrorFilter struct {
	output io.Writer
}

func (f *tlsErrorFilter) Write(p []byte) (n int, err error) {
	msg := string(p)
	// Filter out TLS handshake EOF errors (caused by health checker TCP connects)
	if strings.Contains(msg, "TLS handshake error") && strings.Contains(msg, "EOF") {
		return len(p), nil // Discard the message
	}
	return f.output.Write(p)
}

type Response struct {
	RequestID string `json:"request_id"`
}

type Server struct {
	ServerOptions   *ServerOptions
	Srv             *http.Server
	Logger          *logger.Logger
	Router          *mux.Router
	Middleware      []*mux.MiddlewareFunc
	Websocket       *Websocket
	MessengerClient *messenger.Client
	TrustedAccounts []string
	LetsEncryptMgr  *letsencrypt.Manager
	// NATSAPI is the bridge that serves the HTTP API over NATS once
	// StartNATSAPI (or ServeNATSAccount) has been called; nil until then.
	NATSAPI *natsapi.Service
	natsMu  sync.Mutex
}

func NewServer(options *ServerOptions) (*Server, error) {
	l := logger.NewLogger(options.Application, options.Component)
	server := Server{
		ServerOptions:   options,
		Logger:          l,
		Router:          mux.NewRouter(),
		Middleware:      make([]*mux.MiddlewareFunc, 0),
		Websocket:       NewWebsocket(&WebsocketOptions{}),
		TrustedAccounts: options.TrustedAccounts,
	}
	if options.Messenger {
		err := server.AddMessenger(&messenger.ClientOptions{
			Application:   options.Component,
			Component:     "messenger_client",
			ControllerURL: options.MessengerServerURL,
			AccountID:     options.MessengerAccountID,
			PrivateKey:    options.PrivateKey,
			Certificate:   options.Certificate,
			CA:            options.CA,
			JWT:           options.MessengerJWT,
			Seed:          options.MessengerSeed,
		})
		if err != nil {
			return &server, err
		}
	}

	var tlsConfig *tls.Config

	// Check if Let's Encrypt is enabled for this server
	if options.UseLetsEncrypt() {
		// Check for shared manager first (preferred for all-in-one deployments)
		if options.LetsEncryptManager != nil {
			l.Info("using shared Let's Encrypt certificate manager")
			server.LetsEncryptMgr = options.LetsEncryptManager
			tlsConfig = options.LetsEncryptManager.GetTLSConfig()
		} else if options.LetsEncrypt != nil {
			// Fall back to creating new manager from config
			l.Info("initializing Let's Encrypt certificate manager")
			mgr, err := letsencrypt.NewManager(*options.LetsEncrypt, l.ZeroLogger())
			if err != nil {
				return &server, fmt.Errorf("failed to create Let's Encrypt manager: %w", err)
			}

			// Initialize the ACME client and register account
			if err := mgr.Initialize(); err != nil {
				return &server, fmt.Errorf("failed to initialize Let's Encrypt: %w", err)
			}

			// Ensure we have a valid certificate
			if err := mgr.EnsureCertificate(); err != nil {
				return &server, fmt.Errorf("failed to obtain Let's Encrypt certificate: %w", err)
			}

			server.LetsEncryptMgr = mgr
			tlsConfig = mgr.GetTLSConfig()
			l.Info("Let's Encrypt certificate manager initialized")
		}
	} else {
		// Use static certificates
		staticTLS, err := server.ServerOptions.GetTLSConfig()
		if err != nil {
			return &server, err
		}
		tlsConfig = staticTLS
	}

	// Create error log that filters out TLS handshake EOF errors (health checker noise)
	errorLog := log.New(&tlsErrorFilter{output: log.Writer()}, "", log.LstdFlags)

	server.Srv = &http.Server{
		Addr:      server.ServerOptions.GetConnectionString(),
		Handler:   server.addMiddlewareHandlers(),
		TLSConfig: tlsConfig,
		ErrorLog:  errorLog,
	}
	return &server, nil
}

func (server *Server) AddMessenger(options *messenger.ClientOptions) error {
	mc, err := messenger.NewClient(options)
	if err != nil {
		return err
	}
	server.MessengerClient = mc
	return nil
}

func (server *Server) Serve() {
	// Start Let's Encrypt auto-renewal if enabled and we own the manager
	// (shared managers are managed by the main routine)
	if server.LetsEncryptMgr != nil && server.ServerOptions.LetsEncryptManager == nil {
		server.LetsEncryptMgr.StartAutoRenewal(12 * time.Hour)
		server.Logger.Info("started Let's Encrypt certificate auto-renewal (every 12 hours)")
	}

	if server.ServerOptions.NATSAPI {
		if err := server.StartNATSAPI(); err != nil {
			server.Logger.ErrorE().Err(err).Msg("nats api: not started")
		}
	}

	l, err := net.Listen("tcp", server.Srv.Addr)
	if err != nil {
		server.Logger.Fatal(fmt.Sprintf("error starting tcp listener: %v", err))
	}
	server.Logger.InfoE().Str("addr", server.Srv.Addr).Msg("server listening on")
	server.Srv.ServeTLS(l, "", "")
	server.Logger.Info("server closed")
}

// Stop gracefully stops the server and cleans up resources
func (server *Server) Stop() {
	server.natsMu.Lock()
	if server.NATSAPI != nil {
		server.NATSAPI.Close()
	}
	server.natsMu.Unlock()
	// Only stop auto-renewal if we own the manager (not shared)
	if server.LetsEncryptMgr != nil && server.ServerOptions.LetsEncryptManager == nil {
		server.LetsEncryptMgr.Stop()
		server.Logger.Info("stopped Let's Encrypt certificate auto-renewal")
	}
}

func (server *Server) addMiddlewareHandlers() http.Handler {
	cip := clientip.ClientIP{}
	server.Router.Use(cip.Middleware)
	server.Router.Use(server.Logger.LogRequest)
	// CORS
	headersOk := handlers.AllowedHeaders([]string{"X-Requested-With", "content-type", "authorization", "X-Blackbox-AccountID", "X-Filename", "X-Last-Modified", "X-Subpath"})
	allowedOrigins := server.ServerOptions.AllowedOrigins
	if len(allowedOrigins) == 0 {
		allowedOrigins = []string{"*"}
	}
	originsOk := handlers.AllowedOrigins(allowedOrigins)
	// PATCH is included so a cross-origin SPA (a product's own SPA calling
	// the admin API, for example the assistant's session rename) can send
	// it: a method missing here fails the browser's preflight with 405 and
	// the request never reaches a handler or a log.
	methodsOk := handlers.AllowedMethods([]string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"})
	return handlers.CORS(originsOk, headersOk, methodsOk)(server.Router)
	// Other Middleware
}

var EndpointNotImplemented = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	json.NewEncoder(w).Encode(EndpointNotImplementedResponse)
})

var Noop = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(NoopResponse)
})

// natsAPIService returns the NATS API bridge, creating it on first use with
// the server's full handler chain (client IP, request logging, CORS, and
// every route) so a NATS request is handled exactly like an HTTPS one.
// Callers hold natsMu.
func (server *Server) natsAPIService() (*natsapi.Service, error) {
	if server.NATSAPI != nil {
		return server.NATSAPI, nil
	}
	name := server.ServerOptions.NATSAPIService
	if name == "" {
		name = server.ServerOptions.Component
	}
	svc, err := natsapi.New(natsapi.Options{
		Service:     name,
		Handler:     server.Srv.Handler,
		Version:     server.ServerOptions.NATSAPIVersion,
		Description: server.ServerOptions.NATSAPIDescription,
		Logger:      server.Logger,
	})
	if err != nil {
		return nil, err
	}
	server.NATSAPI = svc
	return svc, nil
}

// StartNATSAPI serves the HTTP API over NATS on the server's messenger
// connection, pinned to the messenger account (ServerOptions.
// MessengerAccountID, else the client's AccountID). It is idempotent and
// is called by Serve when ServerOptions.NATSAPI is set; call it directly
// when the messenger client is attached after Serve (AddMessenger).
func (server *Server) StartNATSAPI() error {
	if server.MessengerClient == nil || server.MessengerClient.Client == nil || server.MessengerClient.Client.Conn == nil {
		return fmt.Errorf("nats api: no messenger client")
	}
	accountID := server.ServerOptions.MessengerAccountID
	if accountID == "" {
		accountID = server.MessengerClient.AccountID
	}
	return server.ServeNATSAccount(accountID, server.MessengerClient.Client.Conn)
}

// ServeNATSAccount serves the HTTP API over NATS on nc, pinned to
// accountID. The bridge is created on first use. Serving an account that
// is already served is a no-op.
func (server *Server) ServeNATSAccount(accountID string, nc *nats.Conn) error {
	server.natsMu.Lock()
	defer server.natsMu.Unlock()
	svc, err := server.natsAPIService()
	if err != nil {
		return err
	}
	return svc.Serve(accountID, nc)
}

// StopNATSAccount stops serving one account; its connection stays open.
func (server *Server) StopNATSAccount(accountID string) error {
	server.natsMu.Lock()
	defer server.natsMu.Unlock()
	if server.NATSAPI == nil {
		return nil
	}
	return server.NATSAPI.Stop(accountID)
}
