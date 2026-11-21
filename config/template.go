package config

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type ConfigTemplate struct {
	App            AppConfig            `json:"app" toml:"app"`
	AdminServer    AdminServerConfig    `json:"admin_server" toml:"admin_server"`
	UI             UIConfig             `json:"ui" toml:"ui"`
	ProxyServer    ProxyServerConfig    `json:"proxy_server" toml:"proxy_server"`
	QueueServer    QueueServerConfig    `json:"queue_server" toml:"queue_server"`
	HostServer     HostServerConfig     `json:"host_server" toml:"host_server"`
	BlackboxServer BlackboxServerConfig `json:"blackbox_server" toml:"blackbox_server"`
	MediaServer    MediaServerConfig    `json:"media_server" toml:"media_server"`
	Controller     ControllerConfig     `json:"controller" toml:"controller"`
	Messenger      MessengerConfig      `json:"messenger" toml:"messenger"`
	Security       SecurityConfig       `json:"security" toml:"security"`
	LetsEncrypt    LetsEncryptConfig    `json:"lets_encrypt" toml:"lets_encrypt"`
	Proxy          ProxyConfig          `json:"proxy" toml:"proxy"`
	Logging        LoggingConfig        `json:"logging" toml:"logging"`
	Metrics        MetricsConfig        `json:"metrics" toml:"metrics"`
}

// LoggingConfig configures centralized log collection.
type LoggingConfig struct {
	// MaxEntries is the maximum number of log entries in the rolling buffer (default 100000)
	MaxEntries int `json:"max_entries" toml:"max_entries"`

	// Level is the initial log level: "debug", "info", "warn", "error" (default "info")
	Level string `json:"level" toml:"level"`
}

// MetricsConfig configures store metrics collection and retention.
type MetricsConfig struct {
	// MaxEntries is the maximum number of metrics entries in the rolling buffer (default 100000)
	MaxEntries int `json:"max_entries" toml:"max_entries"`

	// CollectIntervalSeconds is how often store metrics are collected and published (default 30)
	CollectIntervalSeconds int `json:"collect_interval_seconds" toml:"collect_interval_seconds"`
}

func DefaultConfig(fqdn string, kind string, adminPort int) ConfigTemplate {
	c := New(kind)
	config := ConfigTemplate{}
	config.App = AppConfig{
		Name:     "blackbox",
		RootPath: c.DefaultDataDir(),
		Hostname: fqdn,
	}
	config.AdminServer = AdminServerConfig{
		URL:               fmt.Sprintf("https://%s:%v", fqdn, adminPort),
		AccountServer:     fmt.Sprintf("https://%s:%v", fqdn, adminPort),
		AdminEmail:        "admin@localhost",
		AdminTempPassword: "changeme",
		Port:              adminPort,
	}
	config.UI = UIConfig{
		Port: 8880,
	}
	// this assumes self-signed right now.
	// revisit if certs are passed via cli.
	config.Security = SecurityConfig{
		CertFile: fmt.Sprintf("%s/%s", c.DefaultConfigDir(), "localhost.cert.pem"),
		KeyFile:  fmt.Sprintf("%s/%s", c.DefaultConfigDir(), "localhost.key.pem"),
		CAFile:   fmt.Sprintf("%s/%s", c.DefaultConfigDir(), "localhost.cert.pem"),
	}

	config.Logging = LoggingConfig{
		MaxEntries: 100000,
		Level:      "info",
	}

	config.Metrics = MetricsConfig{
		MaxEntries:             100000,
		CollectIntervalSeconds: 30,
	}

	config.BlackboxServer = BlackboxServerConfig{
		Weather: WeatherConfig{
			Enabled:                 true,
			CurrentIntervalMinutes:  60,
			ForecastIntervalMinutes: 360,
			ForecastDays:            7,
		},
	}

	return config
}

type AppConfig struct {
	Name                 string `json:"name" toml:"name"`
	RootPath             string `json:"root_path" toml:"root_path"`
	Hostname             string `json:"hostname" toml:"hostname"`
	LoginService         string `json:"login_service,omitempty" toml:"login_service,omitempty"`
	AdminService         string `json:"admin_service,omitempty" toml:"admin_service,omitempty"`
	ControllerService    string `json:"controller_service,omitempty" toml:"controller_service,omitempty"`
	ControllerServerName string `json:"controller_server_name" toml:"controller_server_name"`
	ProxyService         string `json:"proxy_service,omitempty" toml:"proxy_service,omitempty"`
	QueueService         string `json:"queue_service,omitempty" toml:"queue_service,omitempty"`
	BlackboxService      string `json:"blackbox_service,omitempty" toml:"blackbox_service,omitempty"`
	MediaService         string `json:"media_service,omitempty" toml:"media_service,omitempty"`
	HostService          string `json:"host_service,omitempty" toml:"host_service,omitempty"`
	CommsService         string `json:"comms_service,omitempty" toml:"comms_service,omitempty"`
	AccountID            string `json:"account_id" toml:"account_id"`
	JWT                  string `json:"jwt" toml:"jwt"`
	Seed                 string `json:"seed" toml:"seed"`
}

type AdminServerConfig struct {
	AdminEmail        string `json:"admin_email" toml:"admin_email"`
	AdminTempPassword string `json:"admin_temp_password" toml:"admin_temp_password"`
	Port              int    `json:"port" toml:"port"`
	AccountServer     string `json:"account_server" toml:"account_server"`
	URL               string `json:"url" toml:"url"`

	// Messenger enables a NATS client on the admin service so it can
	// publish tenant lifecycle events (tenant.notify.created/updated/deleted)
	// for other services to consume. When false, lifecycle events are
	// not published and any subscribers must rely on other discovery
	// mechanisms (e.g., filesystem watchers).
	Messenger bool `json:"messenger,omitempty" toml:"messenger,omitempty"`

	// OAuth Configuration
	OAuthEnabled              bool   `json:"oauth_enabled" toml:"oauth_enabled"`
	GoogleClientID            string `json:"google_client_id,omitempty" toml:"google_client_id,omitempty"`
	GoogleClientSecret        string `json:"google_client_secret,omitempty" toml:"google_client_secret,omitempty"`
	OAuthRedirectURI          string `json:"oauth_redirect_uri,omitempty" toml:"oauth_redirect_uri,omitempty"`
	OAuthStateTTLMinutes      int    `json:"oauth_state_ttl_minutes,omitempty" toml:"oauth_state_ttl_minutes,omitempty"`
	InvitationDefaultTTLHours int    `json:"invitation_default_ttl_hours,omitempty" toml:"invitation_default_ttl_hours,omitempty"`
	AllowSelfServiceAccounts  bool   `json:"allow_self_service_accounts,omitempty" toml:"allow_self_service_accounts,omitempty"`

	// Email (SMTP) Configuration. Authenticated SMTP submission
	// (e.g., Google Workspace via smtp.gmail.com:587 with an app password).
	SMTPHost     string `json:"smtp_host,omitempty" toml:"smtp_host,omitempty"`
	SMTPPort     int    `json:"smtp_port,omitempty" toml:"smtp_port,omitempty"`
	SMTPUsername string `json:"smtp_username,omitempty" toml:"smtp_username,omitempty"`
	SMTPPassword string `json:"smtp_password,omitempty" toml:"smtp_password,omitempty"`
	SMTPFrom     string `json:"smtp_from,omitempty" toml:"smtp_from,omitempty"`
	SMTPFromName string `json:"smtp_from_name,omitempty" toml:"smtp_from_name,omitempty"`

	InviteAcceptURL string `json:"invite_accept_url,omitempty" toml:"invite_accept_url,omitempty"`
}

type UIConfig struct {
	Port int `json:"port" toml:"port"`
}

type ProxyServerConfig struct {
	Port int `json:"port" toml:"port"`
}

type QueueServerConfig struct {
	Port int `json:"port" toml:"port"`
}

type HostServerConfig struct {
	Port int `json:"port" toml:"port"`
}

type BlackboxServerConfig struct {
	Port      int           `json:"port" toml:"port"`
	Messenger bool          `json:"messenger,omitempty" toml:"messenger,omitempty"`
	Weather   WeatherConfig `json:"weather,omitempty" toml:"weather,omitempty"`
}

// WeatherConfig configures the weather collection service.
type WeatherConfig struct {
	// Enabled activates the weather collector (default true)
	Enabled bool `json:"enabled" toml:"enabled"`

	// CurrentIntervalMinutes is how often to fetch current weather (default 60)
	CurrentIntervalMinutes int `json:"current_interval_minutes" toml:"current_interval_minutes"`

	// ForecastIntervalMinutes is how often to refresh forecasts (default 360 = 6 hours)
	ForecastIntervalMinutes int `json:"forecast_interval_minutes" toml:"forecast_interval_minutes"`

	// ForecastDays is how many days of forecast to fetch (default 7)
	ForecastDays int `json:"forecast_days" toml:"forecast_days"`

	// NOAAUserAgent is the User-Agent/contact string required by the NOAA API
	NOAAUserAgent string `json:"noaa_user_agent,omitempty" toml:"noaa_user_agent,omitempty"`
}

type MediaServerConfig struct {
	Port              int                     `json:"port" toml:"port"`
	Messenger         bool                    `json:"messenger,omitempty" toml:"messenger,omitempty"`
	DirectoryProvider DirectoryProviderConfig `json:"directory_provider,omitempty" toml:"directory_provider,omitempty"`
	RemovableProvider RemovableProviderConfig `json:"removable_provider,omitempty" toml:"removable_provider,omitempty"`
}

// DirectoryProviderConfig configures the local directory media provider.
type DirectoryProviderConfig struct {
	// Enabled activates the directory provider
	Enabled bool `json:"enabled" toml:"enabled"`

	// WatchPaths is the list of directories to watch for media files
	WatchPaths []string `json:"watch_paths" toml:"watch_paths"`

	// Recursive determines if subdirectories should be watched
	Recursive bool `json:"recursive" toml:"recursive"`

	// ScanOnStartup determines if initial scan should run at startup
	ScanOnStartup bool `json:"scan_on_startup" toml:"scan_on_startup"`
}

// RemovableProviderConfig configures the removable media auto-import service.
type RemovableProviderConfig struct {
	// Enabled activates the removable media service
	Enabled bool `json:"enabled" toml:"enabled"`

	// MountPaths are parent directories to watch for volume mounts.
	// Defaults to ["/Volumes"] on macOS, ["/media", "/run/media"] on Linux.
	MountPaths []string `json:"mount_paths,omitempty" toml:"mount_paths,omitempty"`

	// AutoScan imports from volumes already mounted at startup
	AutoScan bool `json:"auto_scan" toml:"auto_scan"`

	// PollInterval is seconds between mount checks (default 5)
	PollInterval int `json:"poll_interval" toml:"poll_interval"`
}

type ControllerConfig struct {
	AccountServer     string `json:"account_server" toml:"account_server"`
	AccountServerName string `json:"account_server_name" toml:"account_server_name"` // SNI hostname for TCP proxy
	ServerName        string `json:"server_name" toml:"server_name"`
	URL               string `json:"url" toml:"url"`
	Port              int    `json:"port" toml:"port"`
	MonitoringPort    int    `json:"monitoring_port" toml:"monitoring_port"`
	// Embedded asks a process that hosts the admin server to also run the
	// NATS controller in-process (the standalone cmd/admin honours it; the
	// product binary may always embed it when the routine is enabled).
	Embedded           bool   `json:"embedded" toml:"embedded"`
	SystemAccountID    string `json:"system_account_id" toml:"system_account_id"`
	OperatorID         string `json:"operator_id" toml:"operator_id"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify" toml:"insecure_skip_verify"`
}

type MessengerConfig struct {
	JWT  string `json:"jwt" toml:"jwt"`
	Seed string `json:"seed" toml:"seed"`
}

type SecurityConfig struct {
	// Browser-facing TLS (used when Let's Encrypt is disabled)
	CertFile string `json:"cert_file" toml:"cert_file"`
	KeyFile  string `json:"key_file" toml:"key_file"`
	CAFile   string `json:"ca_file" toml:"ca_file"`

	// Internal PKI for agent-to-server and server-to-server communication
	InternalCertFile string `json:"internal_cert_file,omitempty" toml:"internal_cert_file,omitempty"`
	InternalKeyFile  string `json:"internal_key_file,omitempty" toml:"internal_key_file,omitempty"`
	InternalCAFile   string `json:"internal_ca_file,omitempty" toml:"internal_ca_file,omitempty"`
}

// LetsEncryptConfig configures automatic TLS certificates via Let's Encrypt
// for browser-facing services (UI, Admin API)
type LetsEncryptConfig struct {
	// Enabled activates Let's Encrypt certificate management
	Enabled bool `json:"enabled" toml:"enabled"`

	// Provider is the ACME provider: "letsencrypt" (production) or "letsencrypt-staging" (testing)
	Provider string `json:"provider" toml:"provider"`

	// Email is used for certificate expiration notifications and account recovery
	Email string `json:"email" toml:"email"`

	// Domains is the list of domain names to obtain certificates for
	Domains []string `json:"domains" toml:"domains"`

	// ChallengeType is the ACME challenge type: "dns-01" (recommended) or "http-01"
	ChallengeType string `json:"challenge_type" toml:"challenge_type"`

	// DNSProvider is the DNS provider for DNS-01 challenges (e.g., "cloudflare", "route53")
	DNSProvider string `json:"dns_provider,omitempty" toml:"dns_provider,omitempty"`

	// DNSCredentialsFile is the path to DNS provider credentials
	DNSCredentialsFile string `json:"dns_credentials_file,omitempty" toml:"dns_credentials_file,omitempty"`

	// HTTPPort is the port for HTTP-01 challenges (default 80)
	HTTPPort int `json:"http_port,omitempty" toml:"http_port,omitempty"`

	// CertCachePath is where Let's Encrypt certificates are stored
	CertCachePath string `json:"cert_cache_path" toml:"cert_cache_path"`

	// RenewBeforeDays is how many days before expiry to renew (default 30)
	RenewBeforeDays int `json:"renew_before_days" toml:"renew_before_days"`

	// ApplyTo specifies which services use Let's Encrypt certs: ["ui", "admin", "proxy", "queue", "blackbox"]
	ApplyTo []string `json:"apply_to" toml:"apply_to"`
}

func (configTemplate *ConfigTemplate) WriteToDisk(file string) error {
	b, err := configTemplate.MarshalJSON()
	if err != nil {
		return err
	}
	return ioutil.WriteFile(file, b, 0644)
}

func (configTemplate *ConfigTemplate) MarshalJSON() ([]byte, error) {
	return json.Marshal(*configTemplate)
}

func (configTemplate *ConfigTemplate) WriteToDiskTOML(file string) error {
	b, err := configTemplate.MarshalTOML()
	if err != nil {
		return err
	}
	return ioutil.WriteFile(file, b, 0644)
}

func (configTemplate *ConfigTemplate) MarshalTOML() ([]byte, error) {
	return toml.Marshal(*configTemplate)
}

// ProxyConfig configures the unified TCP proxy for all-in-one deployments.
// When enabled, all backend services are accessed through a single entry point
// using TLS SNI-based routing.
type ProxyConfig struct {
	// Enabled activates the TCP proxy
	Enabled bool `json:"enabled" toml:"enabled"`

	// ListenAddr is the address to listen on (e.g., ":8443")
	ListenAddr string `json:"listen_addr" toml:"listen_addr"`

	// TLS configures TLS certificates for the proxy
	TLS ProxyTLSConfig `json:"tls" toml:"tls"`

	// Routes maps service names to their routing configuration
	Routes map[string]ProxyRouteConfig `json:"routes" toml:"routes"`

	// HealthCheck configures backend health checking
	HealthCheck ProxyHealthCheckConfig `json:"health_check" toml:"health_check"`
}

// ProxyTLSConfig holds TLS certificate configuration for the proxy.
type ProxyTLSConfig struct {
	CertFile string `json:"cert_file" toml:"cert_file"`
	KeyFile  string `json:"key_file" toml:"key_file"`
	CAFile   string `json:"ca_file" toml:"ca_file"`
}

// ProxyRouteConfig defines routing for a single service.
type ProxyRouteConfig struct {
	// SNIHost is the TLS SNI hostname to match (e.g., "admin.blackbox.local")
	SNIHost string `json:"sni_host" toml:"sni_host"`

	// Backends is the list of backend addresses (e.g., ["127.0.0.1:8900"])
	Backends []string `json:"backends" toml:"backends"`

	// TLSPassthrough indicates whether to pass TLS directly to backend
	// (true) or terminate at proxy (false)
	TLSPassthrough bool `json:"tls_passthrough" toml:"tls_passthrough"`
}

// ProxyHealthCheckConfig configures backend health checking.
type ProxyHealthCheckConfig struct {
	// Enabled activates health checking
	Enabled bool `json:"enabled" toml:"enabled"`

	// Interval is how often to check backends
	Interval time.Duration `json:"interval" toml:"interval"`

	// Timeout is the maximum time to wait for a health check
	Timeout time.Duration `json:"timeout" toml:"timeout"`
}
