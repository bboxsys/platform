package controller

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeSelfSignedPEM(t *testing.T, dir string) (string, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		DNSNames:              []string{"localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	return path, cert
}

// A self-signed certificate named in CAFile must verify as a root for the
// resolver connection without disabling verification.
func TestRootPoolWithCATrustsConfiguredCA(t *testing.T) {
	path, cert := writeSelfSignedPEM(t, t.TempDir())
	pool, err := rootPoolWithCA(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, DNSName: "localhost"}); err != nil {
		t.Fatalf("configured CA not trusted: %v", err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{DNSName: "localhost"}); err == nil {
		t.Fatal("expected the system roots alone to reject the self-signed certificate")
	}
}

func TestRootPoolWithCAErrors(t *testing.T) {
	if _, err := rootPoolWithCA(""); err != nil {
		t.Fatalf("empty CAFile should return the system pool: %v", err)
	}
	if _, err := rootPoolWithCA(filepath.Join(t.TempDir(), "missing.pem")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
	empty := filepath.Join(t.TempDir(), "empty.pem")
	if err := os.WriteFile(empty, []byte("not a certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := rootPoolWithCA(empty); err == nil {
		t.Fatal("expected an error for a file without certificates")
	}
}
