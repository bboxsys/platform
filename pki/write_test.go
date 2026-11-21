package pki

import (
	"fmt"
	"testing"
)

func TestWriteCertificateAndPrivateKeyToDisk(t *testing.T) {
	certBytes, keyBytes, err := NewSelfSignedCertificatePair("localhost", "acme")
	if err != nil {
		t.Fatalf("error creating self-signed pair: %v", err)
	}
	fmt.Println("successfully created self-signed certificate pair")
	err = WriteCertificateAndPrivateKeyToDisk(certBytes, keyBytes, "localhost", "./")
	if err != nil {
		t.Fatalf("error creating self-signed pair: %v", err)
	}
	fmt.Println("successfully wrote self-signed certificate pair to local directory")
}
