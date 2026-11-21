package letsencrypt

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/route53"
	"github.com/go-acme/lego/v4/providers/dns/digitalocean"
	"github.com/go-acme/lego/v4/providers/dns/godaddy"
	"github.com/go-acme/lego/v4/providers/dns/namecheap"
)

// DNSCredentials holds credentials for various DNS providers
type DNSCredentials struct {
	// Cloudflare
	CloudflareAPIToken string `json:"cloudflare_api_token,omitempty"`
	CloudflareEmail    string `json:"cloudflare_email,omitempty"`
	CloudflareAPIKey   string `json:"cloudflare_api_key,omitempty"`

	// AWS Route53
	AWSAccessKeyID     string `json:"aws_access_key_id,omitempty"`
	AWSSecretAccessKey string `json:"aws_secret_access_key,omitempty"`
	AWSRegion          string `json:"aws_region,omitempty"`

	// DigitalOcean
	DigitalOceanToken string `json:"digitalocean_token,omitempty"`

	// GoDaddy
	GoDaddyAPIKey    string `json:"godaddy_api_key,omitempty"`
	GoDaddyAPISecret string `json:"godaddy_api_secret,omitempty"`

	// Namecheap
	NamecheapAPIUser string `json:"namecheap_api_user,omitempty"`
	NamecheapAPIKey  string `json:"namecheap_api_key,omitempty"`
}

// SupportedDNSProviders returns a list of supported DNS provider names
func SupportedDNSProviders() []string {
	return []string{
		"cloudflare",
		"route53",
		"digitalocean",
		"godaddy",
		"namecheap",
	}
}

// createDNSProvider creates the appropriate DNS provider based on configuration
func (m *Manager) createDNSProvider() (challenge.Provider, error) {
	if m.config.DNSProvider == "" {
		return nil, fmt.Errorf("DNS provider not specified")
	}

	// Load credentials from file if specified
	var creds DNSCredentials
	if m.config.DNSCredentialsFile != "" {
		data, err := os.ReadFile(m.config.DNSCredentialsFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read DNS credentials file: %w", err)
		}
		if err := json.Unmarshal(data, &creds); err != nil {
			return nil, fmt.Errorf("failed to parse DNS credentials: %w", err)
		}
	}

	switch m.config.DNSProvider {
	case "cloudflare":
		return m.createCloudflareProvider(creds)
	case "route53":
		return m.createRoute53Provider(creds)
	case "digitalocean":
		return m.createDigitalOceanProvider(creds)
	case "godaddy":
		return m.createGoDaddyProvider(creds)
	case "namecheap":
		return m.createNamecheapProvider(creds)
	default:
		return nil, fmt.Errorf("unsupported DNS provider: %s", m.config.DNSProvider)
	}
}

func (m *Manager) createCloudflareProvider(creds DNSCredentials) (challenge.Provider, error) {
	// Prefer API token over email+key
	if creds.CloudflareAPIToken != "" {
		os.Setenv("CF_DNS_API_TOKEN", creds.CloudflareAPIToken)
	} else if creds.CloudflareEmail != "" && creds.CloudflareAPIKey != "" {
		os.Setenv("CF_API_EMAIL", creds.CloudflareEmail)
		os.Setenv("CF_API_KEY", creds.CloudflareAPIKey)
	} else {
		return nil, fmt.Errorf("cloudflare requires either api_token or email+api_key")
	}

	// Use NewDNSProvider() which reads from environment variables we just set
	return cloudflare.NewDNSProvider()
}

func (m *Manager) createRoute53Provider(creds DNSCredentials) (challenge.Provider, error) {
	if creds.AWSAccessKeyID != "" {
		os.Setenv("AWS_ACCESS_KEY_ID", creds.AWSAccessKeyID)
	}
	if creds.AWSSecretAccessKey != "" {
		os.Setenv("AWS_SECRET_ACCESS_KEY", creds.AWSSecretAccessKey)
	}
	if creds.AWSRegion != "" {
		os.Setenv("AWS_REGION", creds.AWSRegion)
	}

	config := route53.NewDefaultConfig()
	return route53.NewDNSProviderConfig(config)
}

func (m *Manager) createDigitalOceanProvider(creds DNSCredentials) (challenge.Provider, error) {
	if creds.DigitalOceanToken == "" {
		return nil, fmt.Errorf("digitalocean requires token")
	}
	os.Setenv("DO_AUTH_TOKEN", creds.DigitalOceanToken)

	config := digitalocean.NewDefaultConfig()
	return digitalocean.NewDNSProviderConfig(config)
}

func (m *Manager) createGoDaddyProvider(creds DNSCredentials) (challenge.Provider, error) {
	if creds.GoDaddyAPIKey == "" || creds.GoDaddyAPISecret == "" {
		return nil, fmt.Errorf("godaddy requires api_key and api_secret")
	}
	os.Setenv("GODADDY_API_KEY", creds.GoDaddyAPIKey)
	os.Setenv("GODADDY_API_SECRET", creds.GoDaddyAPISecret)

	config := godaddy.NewDefaultConfig()
	return godaddy.NewDNSProviderConfig(config)
}

func (m *Manager) createNamecheapProvider(creds DNSCredentials) (challenge.Provider, error) {
	if creds.NamecheapAPIUser == "" || creds.NamecheapAPIKey == "" {
		return nil, fmt.Errorf("namecheap requires api_user and api_key")
	}
	os.Setenv("NAMECHEAP_API_USER", creds.NamecheapAPIUser)
	os.Setenv("NAMECHEAP_API_KEY", creds.NamecheapAPIKey)

	config := namecheap.NewDefaultConfig()
	return namecheap.NewDNSProviderConfig(config)
}
