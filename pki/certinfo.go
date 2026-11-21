package pki

import (
	"crypto/x509"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"
)

// CertInfo holds parsed certificate details for display purposes.
type CertInfo struct {
	Subject      string
	Issuer       string
	SerialNumber *big.Int
	NotBefore    time.Time
	NotAfter     time.Time
	DNSNames     []string
	IPAddresses  []net.IP
	IsCA         bool
	FilePath     string
}

// CertInfoFromFile reads a PEM certificate file and returns its info.
func CertInfoFromFile(certFile string) (*CertInfo, error) {
	cert, err := X509CertificateFromFile(certFile)
	if err != nil {
		return nil, err
	}
	return CertInfoFromX509(cert, certFile), nil
}

// CertInfoFromX509 extracts display info from a parsed x509 certificate.
func CertInfoFromX509(cert *x509.Certificate, filePath string) *CertInfo {
	return &CertInfo{
		Subject:      cert.Subject.String(),
		Issuer:       cert.Issuer.String(),
		SerialNumber: cert.SerialNumber,
		NotBefore:    cert.NotBefore,
		NotAfter:     cert.NotAfter,
		DNSNames:     cert.DNSNames,
		IPAddresses:  cert.IPAddresses,
		IsCA:         cert.IsCA,
		FilePath:     filePath,
	}
}

// Status returns the certificate's validity status.
func (ci *CertInfo) Status() string {
	now := time.Now()
	if now.After(ci.NotAfter) {
		return "EXPIRED"
	}
	if ci.DaysRemaining() <= 30 {
		return "EXPIRING SOON"
	}
	return "valid"
}

// DaysRemaining returns the number of days until the certificate expires.
func (ci *CertInfo) DaysRemaining() int {
	remaining := time.Until(ci.NotAfter)
	if remaining < 0 {
		return 0
	}
	return int(remaining.Hours() / 24)
}

// String returns a formatted multi-line summary of the certificate.
func (ci *CertInfo) String() string {
	var sb strings.Builder

	if ci.FilePath != "" {
		sb.WriteString(fmt.Sprintf("  File:         %s\n", ci.FilePath))
	}
	sb.WriteString(fmt.Sprintf("  Subject:      %s\n", ci.Subject))
	sb.WriteString(fmt.Sprintf("  Issuer:       %s\n", ci.Issuer))
	sb.WriteString(fmt.Sprintf("  Serial:       %s\n", ci.SerialNumber.Text(16)))
	sb.WriteString(fmt.Sprintf("  Not Before:   %s\n", ci.NotBefore.Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("  Not After:    %s\n", ci.NotAfter.Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("  Is CA:        %t\n", ci.IsCA))

	if len(ci.DNSNames) > 0 {
		sb.WriteString(fmt.Sprintf("  DNS SANs:     %s\n", strings.Join(ci.DNSNames, ", ")))
	} else {
		sb.WriteString("  DNS SANs:     (none)\n")
	}

	if len(ci.IPAddresses) > 0 {
		ips := make([]string, len(ci.IPAddresses))
		for i, ip := range ci.IPAddresses {
			ips[i] = ip.String()
		}
		sb.WriteString(fmt.Sprintf("  IP SANs:      %s\n", strings.Join(ips, ", ")))
	} else {
		sb.WriteString("  IP SANs:      (none)\n")
	}

	sb.WriteString(fmt.Sprintf("  Status:       %s (%d days remaining)\n", ci.Status(), ci.DaysRemaining()))

	return sb.String()
}
