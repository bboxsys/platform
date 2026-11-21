package pki

import (
	"fmt"
	"testing"
)

func TestKeyFromFile(t *testing.T) {
	_, _, keyFile := writeIntermediateChain(t)
	_, err := PrivateKeyFromFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("successfully loaded private key from file")
}

func TestTLSCertFromFile(t *testing.T) {
	_, certFile, keyFile := writeIntermediateChain(t)
	_, err := TLSCertFromFile(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("successfully loaded tls cert from files")
}

func TestX509CertificateFromFile(t *testing.T) {
	_, certFile, _ := writeIntermediateChain(t)
	_, err := X509CertificateFromFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("successfully loaded x509 cert from files")
}

func TestPairFromFile(t *testing.T) {
	_, certFile, keyFile := writeIntermediateChain(t)
	_, _, err := PairFromFile(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("successfully loaded pair from files")
}
