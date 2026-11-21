package pki

import (
	"crypto/x509"
	"math/big"
	"net"
	"testing"
	"time"
)

func TestCertInfoFromX509(t *testing.T) {
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(12345),
		NotBefore:    time.Now(),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		DNSNames:     []string{"localhost", "example.com"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:         true,
	}

	info := CertInfoFromX509(cert, "/tmp/test.pem")
	if info.FilePath != "/tmp/test.pem" {
		t.Fatalf("expected file path /tmp/test.pem, got %s", info.FilePath)
	}
	if info.IsCA != true {
		t.Fatal("expected IsCA to be true")
	}
	if len(info.DNSNames) != 2 {
		t.Fatalf("expected 2 DNS names, got %d", len(info.DNSNames))
	}
	if len(info.IPAddresses) != 1 {
		t.Fatalf("expected 1 IP address, got %d", len(info.IPAddresses))
	}
}

func TestCertInfoStatus(t *testing.T) {
	tests := []struct {
		name     string
		notAfter time.Time
		want     string
	}{
		{"valid", time.Now().AddDate(1, 0, 0), "valid"},
		{"expiring soon", time.Now().Add(15 * 24 * time.Hour), "EXPIRING SOON"},
		{"expired", time.Now().Add(-1 * time.Hour), "EXPIRED"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &CertInfo{
				SerialNumber: big.NewInt(1),
				NotAfter:     tt.notAfter,
			}
			if got := info.Status(); got != tt.want {
				t.Fatalf("Status() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCertInfoDaysRemaining(t *testing.T) {
	info := &CertInfo{
		SerialNumber: big.NewInt(1),
		NotAfter:     time.Now().AddDate(0, 0, 100),
	}
	days := info.DaysRemaining()
	if days < 99 || days > 101 {
		t.Fatalf("expected ~100 days remaining, got %d", days)
	}

	expired := &CertInfo{
		SerialNumber: big.NewInt(1),
		NotAfter:     time.Now().Add(-24 * time.Hour),
	}
	if expired.DaysRemaining() != 0 {
		t.Fatalf("expected 0 days remaining for expired cert, got %d", expired.DaysRemaining())
	}
}

func TestCertInfoString(t *testing.T) {
	info := &CertInfo{
		Subject:      "CN=localhost",
		Issuer:       "CN=localhost",
		SerialNumber: big.NewInt(12345),
		NotBefore:    time.Now(),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:         true,
		FilePath:     "/tmp/test.pem",
	}

	output := info.String()
	if output == "" {
		t.Fatal("expected non-empty string output")
	}
	if !contains(output, "localhost") {
		t.Fatal("expected output to contain 'localhost'")
	}
	if !contains(output, "127.0.0.1") {
		t.Fatal("expected output to contain '127.0.0.1'")
	}
}

func TestCertInfoFromFile(t *testing.T) {
	// Generate a cert, write it, then read it back
	certDER, keyDER, err := NewSelfSignedCertificatePair("testhost", "testorg")
	if err != nil {
		t.Fatalf("failed to create self-signed pair: %v", err)
	}

	dir := t.TempDir()
	if err := WriteCertificateAndPrivateKeyToDisk(certDER, keyDER, "testhost", dir); err != nil {
		t.Fatalf("failed to write cert: %v", err)
	}

	certFile := dir + "/testhost.cert.pem"
	info, err := CertInfoFromFile(certFile)
	if err != nil {
		t.Fatalf("CertInfoFromFile failed: %v", err)
	}
	if !contains(info.Subject, "testhost") {
		t.Fatalf("expected subject to contain 'testhost', got %s", info.Subject)
	}
	if info.Status() != "valid" {
		t.Fatalf("expected status 'valid', got %s", info.Status())
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
