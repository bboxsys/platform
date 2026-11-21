package pki

import (
	"crypto/x509"
	"fmt"
	"testing"
	"time"
)

func TestNewSelfSignedCertificatePair(t *testing.T) {
	_, _, err := NewSelfSignedCertificatePair("localhost", "acme")
	if err != nil {
		t.Fatalf("error creating self-signed pair: %v", err)
	}
	fmt.Println("successfully created self-signed certificate pair")
}

func TestNewTLSCertificate(t *testing.T) {
	certificateOptions := CertificateOptions{
		IsSelfSigned: true,
		IsCA:         true,
		CommonName:   "localhost",
		IPSANS:       []string{"127.0.0.1"},
	}
	_, err := NewTLSCertificate(&certificateOptions)
	if err != nil {
		t.Fatalf("error creating new tls certificate: %v", err)
	}
	fmt.Println("successfully created new tls certificate")
}

func TestNewSelfSignedCertificatePairWithOptions(t *testing.T) {
	sans := []string{"example.com", "api.example.com"}
	ipsans := []string{"127.0.0.1", "192.168.1.100"}
	notAfter := time.Now().Add(2 * 365 * 24 * time.Hour) // 2 years

	certDER, keyDER, err := NewSelfSignedCertificatePairWithOptions("myhost", "myorg", sans, ipsans, notAfter)
	if err != nil {
		t.Fatalf("error creating cert with options: %v", err)
	}
	if len(certDER) == 0 {
		t.Fatal("expected non-empty certificate bytes")
	}
	if len(keyDER) == 0 {
		t.Fatal("expected non-empty key bytes")
	}

	// Parse and verify the certificate contents
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("failed to parse generated certificate: %v", err)
	}
	if cert.Subject.CommonName != "myhost" {
		t.Fatalf("expected CN 'myhost', got %q", cert.Subject.CommonName)
	}
	if len(cert.Subject.Organization) == 0 || cert.Subject.Organization[0] != "myorg" {
		t.Fatalf("expected org 'myorg', got %v", cert.Subject.Organization)
	}
	if len(cert.DNSNames) != 2 {
		t.Fatalf("expected 2 DNS SANs, got %d: %v", len(cert.DNSNames), cert.DNSNames)
	}
	if len(cert.IPAddresses) != 2 {
		t.Fatalf("expected 2 IP SANs, got %d: %v", len(cert.IPAddresses), cert.IPAddresses)
	}
	if cert.IPAddresses[1].String() != "192.168.1.100" {
		t.Fatalf("expected IP SAN 192.168.1.100, got %s", cert.IPAddresses[1].String())
	}

	// Verify expiry is approximately 2 years from now
	expectedExpiry := time.Now().Add(2 * 365 * 24 * time.Hour)
	diff := cert.NotAfter.Sub(expectedExpiry)
	if diff < -time.Minute || diff > time.Minute {
		t.Fatalf("expected NotAfter ~%v, got %v (diff: %v)", expectedExpiry, cert.NotAfter, diff)
	}

	fmt.Println("successfully created self-signed certificate pair with options")
}

func TestNewSelfSignedCertificatePairWithOptionsDefaults(t *testing.T) {
	// Zero time should default to 1 year
	certDER, _, err := NewSelfSignedCertificatePairWithOptions("localhost", "acme", nil, nil, time.Time{})
	if err != nil {
		t.Fatalf("error creating cert with default options: %v", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("failed to parse generated certificate: %v", err)
	}

	expectedExpiry := time.Now().AddDate(1, 0, 0)
	diff := cert.NotAfter.Sub(expectedExpiry)
	if diff < -time.Minute || diff > time.Minute {
		t.Fatalf("expected default 1-year expiry, got NotAfter %v (diff: %v)", cert.NotAfter, diff)
	}

	fmt.Println("successfully verified default expiry for cert with options")
}
