package pki

import (
	"fmt"
	"testing"
)

func TestNewCA(t *testing.T) {
	_, err := NewCA("testing", "testing")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("successfully created new CA")
}

func TestCANewTLSCertificate(t *testing.T) {
	ca, err := NewCA("testing", "testing")
	if err != nil {
		t.Fatal(err)
	}
	_, err = ca.NewTLSCertificate("testing", []string{"testing"}, []string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("successfully created new ca tls certificate")
}
