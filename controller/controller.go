////////////////////////////////////////////////////////////////////////
// Embedded NATS Server
////////////////////////////////////////////////////////////////////////

package controller

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/nats-io/jwt/v2"
	natsd "github.com/nats-io/nats-server/v2/server"

	"github.com/bboxsys/platform/logger"
)

type ControllerConfig struct {
	// Application names the hosting process in log lines (app.name in a
	// service config). Empty falls back to "platform".
	Application        string `json:"application"`
	Port               int    `json:"port"`
	MonitorPort        int    `json:"monitor_port"`
	OperatorJWT        string `json:"operator_jwt"`
	SystemAccountID    string `json:"system_account_id"`
	ResolverURL        string `json:"resolver_url"`
	ResolverServerName string `json:"resolver_server_name"` // SNI hostname for TCP proxy
	KeyFile            string `json:"key_file"`
	CertFile           string `json:"cert_file"`
	CAFile             string `json:"ca_file"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify"`
}

type Controller struct {
	// This includes private key and certificate
	Options     *natsd.Options
	natsdserver *natsd.Server
	Logger      *logger.Logger
	mu          sync.Mutex
	ready       bool
}

func (controller *Controller) Ready() bool {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.ready
}
func (controller *Controller) Close() error {
	controller.natsdserver.Shutdown()
	controller.natsdserver.WaitForShutdown()
	return nil
}

// add *opts.Natsd options key fields to server config
func NewController(controllerConfig ControllerConfig) *Controller {
	application := controllerConfig.Application
	if application == "" {
		application = "platform"
	}
	l := logger.NewLogger(application, "nats-server")
	options := &natsd.Options{}
	options.Port = controllerConfig.Port
	options.HTTPPort = controllerConfig.MonitorPort
	options.TrustedOperators = make([]*jwt.OperatorClaims, 0)
	operatorClaims, err := jwt.DecodeOperatorClaims(controllerConfig.OperatorJWT)
	if err != nil {
		l.Fatal(fmt.Sprintf("new controller: error decoding operator claims: %v", err))
	}
	options.TrustedOperators = append(options.TrustedOperators, operatorClaims)
	options.SystemAccount = controllerConfig.SystemAccountID
	accountsResolver, err := natsd.NewURLAccResolver(controllerConfig.ResolverURL)
	if err != nil {
		l.Fatal(fmt.Sprintf("new controller: error creating NewURLAccResolver: %v", err))
	}
	options.AccountResolver = accountsResolver

	// TLS config for the NATS server (client connections)
	tlsConfig, err := natsd.GenTLSConfig(&natsd.TLSConfigOpts{
		CertFile: controllerConfig.CertFile,
		KeyFile:  controllerConfig.KeyFile,
		CaFile:   controllerConfig.CAFile,
		Verify:   true,
		Insecure: false,
	})
	if err != nil {
		l.Fatal(fmt.Sprintf("new controller: error generating TLS config: %v", err))
	}
	options.TLSConfig = tlsConfig

	// TLS config for account resolver (fetching JWTs from admin server)
	// In test mode, skip certificate verification for self-signed certs
	if controllerConfig.InsecureSkipVerify {
		l.Warn("SECURITY: account resolver TLS verification is DISABLED (InsecureSkipVerify=true). This is for test/self-signed environments only and must never be set in production.")
		options.AccountResolverTLSConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	} else {
		// Clone the TLS config to avoid modifying the original
		resolverTLSConfig := tlsConfig.Clone()
		// GenTLSConfig loads CAFile into ClientCAs only, so an outbound
		// resolver connection would verify the admin server against the
		// system roots alone. Trust the configured CA as well, on top of the
		// system roots, so a private or self-signed admin certificate
		// verifies without turning verification off.
		rootCAs, err := rootPoolWithCA(controllerConfig.CAFile)
		if err != nil {
			l.Fatal(fmt.Sprintf("new controller: error loading resolver root CAs: %v", err))
		}
		resolverTLSConfig.RootCAs = rootCAs
		// Set ServerName for SNI if configured (used with TCP proxy)
		if controllerConfig.ResolverServerName != "" {
			l.InfoE().Str("serverName", controllerConfig.ResolverServerName).Msg("using SNI server name for account resolver")
			resolverTLSConfig.ServerName = controllerConfig.ResolverServerName
		}
		options.AccountResolverTLSConfig = resolverTLSConfig
	}

	s, err := natsd.NewServer(options)
	if err != nil {
		l.Fatal(fmt.Sprintf("new controller: error creating NewServer: %v", err))
	}
	return &Controller{
		Options:     options,
		natsdserver: s,
		Logger:      l,
	}
}

func (controller *Controller) Start() {
	go controller.natsdserver.Start()
	controller.Logger.Info("attempting to start controller within 10 seconds")
	if !controller.natsdserver.ReadyForConnections(10 * time.Second) {
		controller.Logger.Fatal("not able to start controller in 10 seconds")
	}
	controller.SetReady()
	controller.Logger.InfoE().Any("host", controller.Options.Host).Any("port", controller.Options.Port).Msg("controller started")
}

func (controller *Controller) SetReady() {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	controller.ready = true
}

// rootPoolWithCA returns the system root pool with the certificates in
// caFile added. An empty caFile returns the system pool unchanged.
func rootPoolWithCA(caFile string) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if caFile == "" {
		return pool, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no certificates found in %s", caFile)
	}
	return pool, nil
}
