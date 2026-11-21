package pki

import (
	"fmt"
	"testing"
)

func TestNewIntermediate(t *testing.T) {
	caFile, certFile, keyFile := writeIntermediateChain(t)
	_, err := NewIntermediate(caFile, certFile, keyFile, "testing")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("successfully created new intermediate")
}

func TestIntermediateNewTLSCertificate(t *testing.T) {
	caFile, certFile, keyFile := writeIntermediateChain(t)
	intermediate, err := NewIntermediate(caFile, certFile, keyFile, "testing")
	if err != nil {
		t.Fatal(err)
	}
	_, err = intermediate.NewTLSCertificate("testing", []string{"testing"}, []string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("successfully created new intermediate tls certificate")
}
