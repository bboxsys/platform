package pki

import (
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"os"
)

func PairFromFile(certFile, keyFile string) (*x509.Certificate, crypto.PrivateKey, error) {
	certificate, err := X509CertificateFromFile(certFile)
	if err != nil {
		return &x509.Certificate{}, nil, err
	}
	privateKey, err := PrivateKeyFromFile(keyFile)
	if err != nil {
		return &x509.Certificate{}, nil, err
	}
	return certificate, privateKey, nil
}

func PrivateKeyFromFile(keyFile string) (crypto.PrivateKey, error) {
	keyPEMBlock, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	return ParsePrivateKeyFromPEM(keyPEMBlock)
}

func X509CertificateFromFile(certFile string) (*x509.Certificate, error) {
	certPEMBlock, err := os.ReadFile(certFile)
	if err != nil {
		return &x509.Certificate{}, err
	}
	return ParseCertificatePEM(certPEMBlock)
}

func TLSCertFromFile(certFile, keyFile string) (tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, err
	}
	return cert, nil
}
