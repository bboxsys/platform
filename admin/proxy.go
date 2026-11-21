package admin

import "time"

// ProxyConfig holds the configuration for the TCP proxy service.
// This is stored in the admin service for persistence and runtime updates.
type ProxyConfig struct {
	Enabled     bool                  `json:"enabled"`
	ListenAddr  string                `json:"listen_addr"`
	TLS         ProxyTLSConfig        `json:"tls"`
	Routes      map[string]ProxyRoute `json:"routes"`
	HealthCheck ProxyHealthCheck      `json:"health_check"`
}

// ProxyTLSConfig holds TLS certificate configuration.
type ProxyTLSConfig struct {
	CertFile string `json:"cert_file"`
	KeyFile  string `json:"key_file"`
	CAFile   string `json:"ca_file"`
}

// ProxyRoute defines routing for a single service.
type ProxyRoute struct {
	SNIHost        string   `json:"sni_host"`
	Backends       []string `json:"backends"`
	TLSPassthrough bool     `json:"tls_passthrough"`
}

// ProxyHealthCheck configures backend health checking.
type ProxyHealthCheck struct {
	Enabled  bool          `json:"enabled"`
	Interval time.Duration `json:"interval"`
	Timeout  time.Duration `json:"timeout"`
}

// ProxyStatus represents the runtime status of the proxy.
type ProxyStatus struct {
	Running    bool                      `json:"running"`
	ListenAddr string                    `json:"listen_addr"`
	RouteCount int                       `json:"route_count"`
	Backends   map[string]*BackendHealth `json:"backends"`
}

// BackendHealth represents the health status of a backend.
type BackendHealth struct {
	Address   string    `json:"address"`
	Healthy   bool      `json:"healthy"`
	LastCheck time.Time `json:"last_check"`
	Error     string    `json:"error,omitempty"`
}
