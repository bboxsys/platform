package pki

import (
	"crypto/x509"
	"path/filepath"
	"testing"
)

// writeIntermediateChain generates a self-signed CA and an intermediate
// certificate/key signed by it, writes all three PEM files into t.TempDir(),
// and returns their paths. This lets the intermediate and load tests run
// self-contained instead of depending on INTERMEDIATE_CA_FILE /
// INTERMEDIATE_CERT_FILE / INTERMEDIATE_KEY_FILE pointing at pre-existing
// files (which failed with "open : no such file or directory" when unset).
func writeIntermediateChain(t *testing.T) (caFile, certFile, keyFile string) {
	t.Helper()

	ca, err := NewCA("testca", "testing")
	if err != nil {
		t.Fatalf("error creating CA: %v", err)
	}

	intCert, intKey, err := ca.NewIntermediateX509Certificate("intermediate", []string{"intermediate"}, []string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("error creating intermediate certificate: %v", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(intKey)
	if err != nil {
		t.Fatalf("error marshaling intermediate private key: %v", err)
	}

	dir := t.TempDir()
	caFile = filepath.Join(dir, "ca.cert.pem")
	certFile = filepath.Join(dir, "intermediate.cert.pem")
	keyFile = filepath.Join(dir, "intermediate.key.pem")

	if err := WritePEMToFile(ca.Certificate.Raw, "CERTIFICATE", caFile, 0644); err != nil {
		t.Fatalf("error writing CA certificate: %v", err)
	}
	if err := WriteCertAndKeyToFiles(intCert.Raw, keyDER, certFile, keyFile); err != nil {
		t.Fatalf("error writing intermediate certificate/key: %v", err)
	}
	return caFile, certFile, keyFile
}
