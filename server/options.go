package server

import (
	"crypto/tls"
	"strconv"

	"github.com/bboxsys/platform/config"
	"github.com/bboxsys/platform/letsencrypt"
)

// Logger per server
type ServerOptions struct {
	Application string `json:"application"`
	Component   string `json:"component"`

	// Static TLS certificates (used when LetsEncrypt is not enabled)
	PrivateKey  []byte `json:"private_key"`
	Certificate []byte `json:"certificate"`
	CA          []byte `json:"ca"`

	// Let's Encrypt configuration (optional - if enabled, overrides static certs)
	LetsEncrypt *config.LetsEncryptConfig `json:"lets_encrypt,omitempty"`

	// Shared Let's Encrypt manager (preferred over LetsEncrypt config for all-in-one deployments)
	LetsEncryptManager *letsencrypt.Manager `json:"-"`

	HTTPPort            int      `json:"http_port"`
	PublicKey           string   `json:"public_key"`
	Messenger           bool     `json:"messenger"`
	MessengerAccountID  string   `json:"messenger_account_id"`
	MessengerServerURL  string   `json:"messenger_server_url"`
	MessengerServerName string   `json:"messenger_server_name"`
	MessengerJWT        string   `json:"messenger_jwt"`
	MessengerSeed       string   `json:"messenger_seed"`
	TrustedAccounts     []string `json:"trusted_accounts"`
	BasicAuth           bool     `json:"basic_auth"`
	// NATSAPI serves the HTTP API over NATS as well (package natsapi):
	// the same routes, verbs, payloads, and auth wrappers, reachable by
	// any caller inside the messenger account with only the controller URL
	// and its own credentials. It needs a messenger client.
	NATSAPI bool `json:"nats_api"`
	// NATSAPIService is the subject token and micro service name
	// (api.{service}.{METHOD}.{resource}). Default: Component.
	NATSAPIService string `json:"nats_api_service"`
	// NATSAPIVersion is the micro service version (semver). Default 1.0.0.
	NATSAPIVersion string `json:"nats_api_version"`
	// NATSAPIDescription is published in the micro service info.
	NATSAPIDescription string `json:"nats_api_description"`
	// AllowedOrigins configures the CORS allowlist. When empty it defaults to
	// ["*"]. Auth is header-based (bearer/basic, not cookies) so a wildcard
	// does not leak credentials, but production deployments should pin their
	// own origins here to narrow the surface.
	AllowedOrigins []string `json:"allowed_origins"`
	// this is for http authZ
	Metadata map[string]interface{} `json:"metadata"`
}

// UseLetsEncrypt returns true if Let's Encrypt is configured and enabled
func (options *ServerOptions) UseLetsEncrypt() bool {
	// Check for shared manager first (preferred for all-in-one deployments)
	if options.LetsEncryptManager != nil {
		return true
	}
	return options.LetsEncrypt != nil && options.LetsEncrypt.Enabled
}

func DefaultServerOptions() *ServerOptions {
	options := ServerOptions{}
	return &options
}

// Get metadata value from key. Returns nil if key does not exist.
func (options *ServerOptions) GetMetadata(key string) interface{} {
	if options.Metadata == nil {
		return nil
	}
	if val, ok := options.Metadata[key]; ok {
		return val
	}
	return nil
}

// This is for HTTP server
func (options *ServerOptions) GetConnectionString() string {
	return ":" + strconv.Itoa(options.HTTPPort)
}

func (options *ServerOptions) GetCertificate() (tls.Certificate, error) {
	certificate, err := tls.X509KeyPair(options.Certificate, options.PrivateKey)
	return certificate, err
}

// GetTLSConfig returns a *tls.Config for the static certificate. It returns a
// pointer (not a value) because tls.Config contains a sync.RWMutex and must not
// be copied.
func (options *ServerOptions) GetTLSConfig() (*tls.Config, error) {
	tlsConfig := &tls.Config{}
	certificate, err := options.GetCertificate()
	if err != nil {
		return nil, err
	}
	tlsConfig.Certificates = append(tlsConfig.Certificates, certificate)
	return tlsConfig, nil
}

type WebsocketOptions struct {
	Application string `json:"application"`
}
