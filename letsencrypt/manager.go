// Package letsencrypt provides automatic TLS certificate management via Let's Encrypt
// using the ACME protocol. It supports both DNS-01 and HTTP-01 challenges.
package letsencrypt

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/go-acme/lego/v4/challenge/http01"
	"github.com/go-acme/lego/v4/lego"
	legolog "github.com/go-acme/lego/v4/log"
	"github.com/go-acme/lego/v4/registration"
	"github.com/rs/zerolog"

	"github.com/bboxsys/platform/config"
)

const (
	// ACME directory URLs
	LetsEncryptProduction = "https://acme-v02.api.letsencrypt.org/directory"
	LetsEncryptStaging    = "https://acme-staging-v02.api.letsencrypt.org/directory"

	// Default values
	DefaultRenewBeforeDays = 30
	DefaultHTTPPort        = 80
)

// Manager handles Let's Encrypt certificate acquisition and renewal
type Manager struct {
	config     config.LetsEncryptConfig
	client     *lego.Client
	user       *User
	logger     zerolog.Logger
	certMu     sync.RWMutex
	cert       *tls.Certificate
	certPath   string
	keyPath    string
	renewalMu  sync.Mutex
	stopCh     chan struct{}
}

// User implements acme.User for lego
type User struct {
	Email        string
	Registration *registration.Resource
	key          crypto.PrivateKey
}

func (u *User) GetEmail() string {
	return u.Email
}

func (u *User) GetRegistration() *registration.Resource {
	return u.Registration
}

func (u *User) GetPrivateKey() crypto.PrivateKey {
	return u.key
}

// NewManager creates a new Let's Encrypt certificate manager
func NewManager(cfg config.LetsEncryptConfig, logger zerolog.Logger) (*Manager, error) {
	if !cfg.Enabled {
		return nil, fmt.Errorf("Let's Encrypt is not enabled")
	}

	if cfg.Email == "" {
		return nil, fmt.Errorf("email is required for Let's Encrypt")
	}

	if len(cfg.Domains) == 0 {
		return nil, fmt.Errorf("at least one domain is required")
	}

	if cfg.CertCachePath == "" {
		return nil, fmt.Errorf("cert cache path is required")
	}

	// Ensure cache directory exists
	if err := os.MkdirAll(cfg.CertCachePath, 0700); err != nil {
		return nil, fmt.Errorf("failed to create cert cache directory: %w", err)
	}

	m := &Manager{
		config: cfg,
		logger: logger,
		stopCh: make(chan struct{}),
	}

	// Configure lego to use our zerolog-based logger
	legolog.Logger = NewLegoLogger(logger)

	// Set defaults
	if m.config.RenewBeforeDays == 0 {
		m.config.RenewBeforeDays = DefaultRenewBeforeDays
	}

	if m.config.HTTPPort == 0 {
		m.config.HTTPPort = DefaultHTTPPort
	}

	// Set certificate paths
	m.certPath = filepath.Join(cfg.CertCachePath, cfg.Domains[0]+".crt")
	m.keyPath = filepath.Join(cfg.CertCachePath, cfg.Domains[0]+".key")

	return m, nil
}

// Initialize sets up the ACME client and registers the account
func (m *Manager) Initialize() error {
	// Load or create user account
	user, err := m.loadOrCreateUser()
	if err != nil {
		return fmt.Errorf("failed to load/create user: %w", err)
	}
	m.user = user

	// Determine ACME directory URL
	directoryURL := LetsEncryptProduction
	if m.config.Provider == "letsencrypt-staging" {
		directoryURL = LetsEncryptStaging
		m.logger.Info().Msg("Using Let's Encrypt staging environment")
	}

	// Create lego config
	legoConfig := lego.NewConfig(m.user)
	legoConfig.CADirURL = directoryURL
	legoConfig.Certificate.KeyType = certcrypto.EC256

	// Create client
	client, err := lego.NewClient(legoConfig)
	if err != nil {
		return fmt.Errorf("failed to create ACME client: %w", err)
	}
	m.client = client

	// Configure challenge provider
	if err := m.configureChallenge(); err != nil {
		return fmt.Errorf("failed to configure challenge: %w", err)
	}

	// Register account if needed
	if m.user.Registration == nil {
		reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			return fmt.Errorf("failed to register account: %w", err)
		}
		m.user.Registration = reg
		if err := m.saveUser(); err != nil {
			m.logger.Warn().Err(err).Msg("Failed to save user registration")
		}
		m.logger.Info().Str("email", m.user.Email).Msg("Registered new ACME account")
	}

	return nil
}

// configureChallenge sets up the appropriate ACME challenge provider
func (m *Manager) configureChallenge() error {
	switch m.config.ChallengeType {
	case "http-01":
		provider := http01.NewProviderServer("", fmt.Sprintf("%d", m.config.HTTPPort))
		return m.client.Challenge.SetHTTP01Provider(provider)

	case "dns-01":
		provider, err := m.createDNSProvider()
		if err != nil {
			return fmt.Errorf("failed to create DNS provider: %w", err)
		}
		// Use system DNS resolver (reads from /etc/resolv.conf)
		// DisableCompletePropagationRequirement skips direct authoritative nameserver checks
		// which may be blocked by firewalls - only uses the system's recursive resolver
		// PropagationWait adds delay to allow global DNS propagation before verification
		return m.client.Challenge.SetDNS01Provider(provider,
			dns01.DisableCompletePropagationRequirement(),
			dns01.PropagationWait(60*time.Second, false))

	default:
		return fmt.Errorf("unsupported challenge type: %s", m.config.ChallengeType)
	}
}

// ObtainCertificate obtains a new certificate for the configured domains
func (m *Manager) ObtainCertificate() error {
	m.renewalMu.Lock()
	defer m.renewalMu.Unlock()

	m.logger.Info().Strs("domains", m.config.Domains).Msg("Obtaining certificate")

	request := certificate.ObtainRequest{
		Domains: m.config.Domains,
		Bundle:  true,
	}

	certificates, err := m.client.Certificate.Obtain(request)
	if err != nil {
		return fmt.Errorf("failed to obtain certificate: %w", err)
	}

	// Save certificate and key
	if err := os.WriteFile(m.certPath, certificates.Certificate, 0600); err != nil {
		return fmt.Errorf("failed to save certificate: %w", err)
	}

	if err := os.WriteFile(m.keyPath, certificates.PrivateKey, 0600); err != nil {
		return fmt.Errorf("failed to save private key: %w", err)
	}

	// Load into memory
	if err := m.loadCertificate(); err != nil {
		return fmt.Errorf("failed to load certificate: %w", err)
	}

	m.logger.Info().
		Strs("domains", m.config.Domains).
		Str("cert_path", m.certPath).
		Msg("Certificate obtained and saved")

	return nil
}

// RenewCertificate renews the current certificate if needed
func (m *Manager) RenewCertificate() error {
	m.renewalMu.Lock()
	defer m.renewalMu.Unlock()

	// Check if renewal is needed
	needsRenewal, err := m.needsRenewal()
	if err != nil {
		return err
	}

	if !needsRenewal {
		m.logger.Debug().Msg("Certificate does not need renewal")
		return nil
	}

	m.logger.Info().Strs("domains", m.config.Domains).Msg("Renewing certificate")

	// Load existing certificate for renewal
	certPEM, err := os.ReadFile(m.certPath)
	if err != nil {
		// No existing cert, obtain new one
		m.renewalMu.Unlock()
		err = m.ObtainCertificate()
		m.renewalMu.Lock()
		return err
	}

	keyPEM, err := os.ReadFile(m.keyPath)
	if err != nil {
		return fmt.Errorf("failed to read private key: %w", err)
	}

	certResource := &certificate.Resource{
		Domain:      m.config.Domains[0],
		Certificate: certPEM,
		PrivateKey:  keyPEM,
	}

	// Renew the certificate
	newCert, err := m.client.Certificate.Renew(*certResource, true, false, "")
	if err != nil {
		return fmt.Errorf("failed to renew certificate: %w", err)
	}

	// Save new certificate
	if err := os.WriteFile(m.certPath, newCert.Certificate, 0600); err != nil {
		return fmt.Errorf("failed to save renewed certificate: %w", err)
	}

	if err := os.WriteFile(m.keyPath, newCert.PrivateKey, 0600); err != nil {
		return fmt.Errorf("failed to save renewed private key: %w", err)
	}

	// Reload certificate
	if err := m.loadCertificate(); err != nil {
		return fmt.Errorf("failed to reload certificate: %w", err)
	}

	m.logger.Info().Strs("domains", m.config.Domains).Msg("Certificate renewed successfully")

	return nil
}

// needsRenewal checks if the certificate needs to be renewed
func (m *Manager) needsRenewal() (bool, error) {
	certPEM, err := os.ReadFile(m.certPath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil // No certificate, needs to obtain
		}
		return false, err
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		return true, nil
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return true, nil
	}

	// Check if certificate expires within the renewal window
	renewalTime := cert.NotAfter.Add(-time.Duration(m.config.RenewBeforeDays) * 24 * time.Hour)
	return time.Now().After(renewalTime), nil
}

// loadCertificate loads the certificate from disk into memory
func (m *Manager) loadCertificate() error {
	cert, err := tls.LoadX509KeyPair(m.certPath, m.keyPath)
	if err != nil {
		return err
	}

	m.certMu.Lock()
	m.cert = &cert
	m.certMu.Unlock()

	return nil
}

// GetCertificate returns the current TLS certificate for use with tls.Config.GetCertificate
func (m *Manager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	m.certMu.RLock()
	defer m.certMu.RUnlock()

	if m.cert == nil {
		return nil, fmt.Errorf("no certificate available")
	}

	return m.cert, nil
}

// GetTLSConfig returns a tls.Config configured to use Let's Encrypt certificates
func (m *Manager) GetTLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: m.GetCertificate,
		MinVersion:     tls.VersionTLS12,
	}
}

// StartAutoRenewal starts a background goroutine that checks for certificate renewal
func (m *Manager) StartAutoRenewal(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := m.RenewCertificate(); err != nil {
					m.logger.Error().Err(err).Msg("Failed to renew certificate")
				}
			case <-m.stopCh:
				m.logger.Info().Msg("Stopping certificate auto-renewal")
				return
			}
		}
	}()

	m.logger.Info().Dur("interval", interval).Msg("Started certificate auto-renewal")
}

// Stop stops the auto-renewal goroutine
func (m *Manager) Stop() {
	close(m.stopCh)
}

// EnsureCertificate ensures a valid certificate exists, obtaining one if necessary
func (m *Manager) EnsureCertificate() error {
	// Try to load existing certificate
	if err := m.loadCertificate(); err == nil {
		// Certificate loaded, check if renewal is needed
		needsRenewal, _ := m.needsRenewal()
		if !needsRenewal {
			m.logger.Info().Msg("Loaded existing certificate")
			return nil
		}
		m.logger.Info().Msg("Existing certificate needs renewal")
	}

	// Obtain new certificate
	return m.ObtainCertificate()
}

// loadOrCreateUser loads the ACME user from disk or creates a new one
func (m *Manager) loadOrCreateUser() (*User, error) {
	accountPath := filepath.Join(m.config.CertCachePath, "account.json")
	keyPath := filepath.Join(m.config.CertCachePath, "account.key")

	// Try to load existing user
	if _, err := os.Stat(keyPath); err == nil {
		keyPEM, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, err
		}

		block, _ := pem.Decode(keyPEM)
		if block == nil {
			return nil, fmt.Errorf("failed to decode account key")
		}

		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}

		user := &User{
			Email: m.config.Email,
			key:   key,
		}

		// Load registration if exists
		if regData, err := os.ReadFile(accountPath); err == nil {
			// Registration data exists but we'll let lego handle re-registration
			_ = regData
		}

		return user, nil
	}

	// Create new user with new key
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate account key: %w", err)
	}

	user := &User{
		Email: m.config.Email,
		key:   privateKey,
	}

	// Save the key
	keyBytes, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return nil, err
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: keyBytes,
	})

	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return nil, fmt.Errorf("failed to save account key: %w", err)
	}

	return user, nil
}

// saveUser saves the user registration to disk
func (m *Manager) saveUser() error {
	// For now, the registration is handled by lego internally
	// We just save the key which is done in loadOrCreateUser
	return nil
}

// CertPath returns the path to the certificate file
func (m *Manager) CertPath() string {
	return m.certPath
}

// KeyPath returns the path to the private key file
func (m *Manager) KeyPath() string {
	return m.keyPath
}
