package admin

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/bboxsys/platform/server"
)

// PKIStatus represents the overall PKI configuration and status
type PKIStatus struct {
	Enabled           bool               `json:"enabled"`
	LetsEncryptConfig *LetsEncryptStatus `json:"lets_encrypt,omitempty"`
	InternalPKI       *InternalPKIStatus `json:"internal_pki,omitempty"`
	Certificates      []CertificateInfo  `json:"certificates"`
}

// LetsEncryptStatus represents Let's Encrypt configuration
type LetsEncryptStatus struct {
	Enabled       bool     `json:"enabled"`
	Provider      string   `json:"provider"`
	Domains       []string `json:"domains"`
	Email         string   `json:"email"`
	ChallengeType string   `json:"challenge_type"`
	CertCachePath string   `json:"cert_cache_path"`
}

// InternalPKIStatus represents internal PKI configuration
type InternalPKIStatus struct {
	CertFile string `json:"cert_file"`
	KeyFile  string `json:"key_file"`
	CAFile   string `json:"ca_file"`
	HasCA    bool   `json:"has_ca"`
	HasCert  bool   `json:"has_cert"`
	HasKey   bool   `json:"has_key"`
}

// CertificateInfo represents information about a certificate
type CertificateInfo struct {
	ID            string    `json:"id"`
	Subject       string    `json:"subject"`
	Issuer        string    `json:"issuer"`
	SerialNumber  string    `json:"serial_number"`
	NotBefore     time.Time `json:"not_before"`
	NotAfter      time.Time `json:"not_after"`
	DNSNames      []string  `json:"dns_names,omitempty"`
	IPAddresses   []string  `json:"ip_addresses,omitempty"`
	IsCA          bool      `json:"is_ca"`
	Type          string    `json:"type"`   // "server", "ca", "intermediate", "agent", "letsencrypt"
	Status        string    `json:"status"` // "valid", "expiring", "expired"
	DaysRemaining int       `json:"days_remaining"`
	FilePath      string    `json:"file_path,omitempty"`
}

// PKISettings represents editable PKI settings
type PKISettings struct {
	LetsEncrypt struct {
		Enabled         bool     `json:"enabled"`
		Provider        string   `json:"provider"`
		Email           string   `json:"email"`
		Domains         []string `json:"domains"`
		ChallengeType   string   `json:"challenge_type"`
		RenewBeforeDays int      `json:"renew_before_days"`
	} `json:"lets_encrypt"`
}

// getMetadataString safely gets a string value from metadata
func getMetadataString(opts *server.ServerOptions, key string) string {
	val := opts.GetMetadata(key)
	if val == nil {
		return ""
	}
	if s, ok := val.(string); ok {
		return s
	}
	return ""
}

// GetPKIStatus returns the current PKI status and configuration
func (adminServer *AdminServer) GetPKIStatus(w http.ResponseWriter, r *http.Request) {
	status := PKIStatus{
		Enabled:      true,
		Certificates: []CertificateInfo{},
	}

	// Check internal PKI configuration
	certFile := getMetadataString(adminServer.ServerOptions, "cert_file")
	keyFile := getMetadataString(adminServer.ServerOptions, "key_file")
	caFile := getMetadataString(adminServer.ServerOptions, "ca_file")

	if certFile != "" || keyFile != "" || caFile != "" {
		status.InternalPKI = &InternalPKIStatus{
			CertFile: certFile,
			KeyFile:  keyFile,
			CAFile:   caFile,
			HasCert:  fileExists(certFile),
			HasKey:   fileExists(keyFile),
			HasCA:    fileExists(caFile),
		}

		// Load and parse certificates
		if status.InternalPKI.HasCert {
			if certInfo, err := parseCertificateFile(certFile, "server"); err == nil {
				status.Certificates = append(status.Certificates, *certInfo)
			}
		}
		if status.InternalPKI.HasCA && caFile != certFile {
			if certInfo, err := parseCertificateFile(caFile, "ca"); err == nil {
				status.Certificates = append(status.Certificates, *certInfo)
			}
		}
	}

	// Check intermediate CA if configured
	if adminServer.intermediateCA != nil {
		intermediateCertFile := getMetadataString(adminServer.ServerOptions, "intermediate_cert_file")
		if intermediateCertFile != "" {
			if certInfo, err := parseCertificateFile(intermediateCertFile, "intermediate"); err == nil {
				status.Certificates = append(status.Certificates, *certInfo)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(status)
}

// GetPKICertificates returns all known certificates
func (adminServer *AdminServer) GetPKICertificates(w http.ResponseWriter, r *http.Request) {
	certificates := []CertificateInfo{}

	// Get certificates from configured paths
	certFile := getMetadataString(adminServer.ServerOptions, "cert_file")
	caFile := getMetadataString(adminServer.ServerOptions, "ca_file")

	if certFile != "" && fileExists(certFile) {
		if certInfo, err := parseCertificateFile(certFile, "server"); err == nil {
			certificates = append(certificates, *certInfo)
		}
	}

	if caFile != "" && caFile != certFile && fileExists(caFile) {
		if certInfo, err := parseCertificateFile(caFile, "ca"); err == nil {
			certificates = append(certificates, *certInfo)
		}
	}

	// Check intermediate CA
	intermediateCertFile := getMetadataString(adminServer.ServerOptions, "intermediate_cert_file")
	if intermediateCertFile != "" && fileExists(intermediateCertFile) {
		if certInfo, err := parseCertificateFile(intermediateCertFile, "intermediate"); err == nil {
			certificates = append(certificates, *certInfo)
		}
	}

	// Get agent certificates from store (issued certificates)
	accountID := AccountIDFromContext(r.Context())
	if accountID != "" {
		agents, err := adminServer.st.GetAgents(accountID)
		if err == nil && agents != nil {
			for _, agent := range *agents {
				// If we have certificate info stored for agents, add them here
				if agent.ID != "" {
					certificates = append(certificates, CertificateInfo{
						ID:      agent.ID,
						Subject: fmt.Sprintf("CN=%s", agent.Hostname),
						Type:    "agent",
						Status:  "valid", // Would need to track actual cert expiry
					})
				}
			}
		}
	}

	// Configured certificates first, then agents, in the order gathered
	// above. See writeList for paging.
	writeList(w, r, certificates, nil)
}

// GetPKISettings returns the current PKI settings
func (adminServer *AdminServer) GetPKISettings(w http.ResponseWriter, r *http.Request) {
	settings := PKISettings{}

	// These would come from the server configuration
	// For now, return empty/defaults

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(settings)
}

// Helper functions

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func parseCertificateFile(path string, certType string) (*CertificateInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	daysRemaining := int(cert.NotAfter.Sub(now).Hours() / 24)

	status := "valid"
	if now.After(cert.NotAfter) {
		status = "expired"
	} else if daysRemaining <= 30 {
		status = "expiring"
	}

	ipAddresses := make([]string, len(cert.IPAddresses))
	for i, ip := range cert.IPAddresses {
		ipAddresses[i] = ip.String()
	}

	return &CertificateInfo{
		ID:            cert.SerialNumber.String(),
		Subject:       cert.Subject.String(),
		Issuer:        cert.Issuer.String(),
		SerialNumber:  cert.SerialNumber.String(),
		NotBefore:     cert.NotBefore,
		NotAfter:      cert.NotAfter,
		DNSNames:      cert.DNSNames,
		IPAddresses:   ipAddresses,
		IsCA:          cert.IsCA,
		Type:          certType,
		Status:        status,
		DaysRemaining: daysRemaining,
		FilePath:      path,
	}, nil
}
