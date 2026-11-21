package pki

import (
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"time"
)

// The CA should be offline unless generating certificates for intermediate
// certificate authorities.
type CA struct {
	Certificate *x509.Certificate
	PrivateKey  crypto.PrivateKey
	Org         string
}

func NewCA(name, org string) (*CA, error) {
	certificateOptions := CertificateOptions{
		Org:          org,
		CommonName:   name,
		SANS:         []string{name},
		IsCA:         true,
		IsSelfSigned: true,
		NotAfter:     time.Now().AddDate(10, 0, 0),
	}
	certificate, privateKey, err := NewX509Certificate(&certificateOptions)
	if err != nil {
		return &CA{}, err
	}
	return &CA{
		Certificate: certificate,
		PrivateKey:  privateKey,
		Org:         org,
	}, nil
}

func (ca *CA) NewIntermediateX509Certificate(cn string, sans, ipsans []string) (*x509.Certificate, crypto.PrivateKey, error) {
	certificateOptions := CertificateOptions{
		SigningKey:   ca.PrivateKey,
		IsSelfSigned: false,
		SigningCert:  ca.Certificate,
		Org:          ca.Org,
		CommonName:   cn,
		SANS:         sans,
		IPSANS:       ipsans,
		IsCA:         true,
		NotAfter:     time.Now().AddDate(10, 0, 0),
	}
	return NewX509Certificate(&certificateOptions)
}
func (ca *CA) NewCertificate(cn string, sans, ipsans []string) (Certificate, PrivateKey, error) {
	certificateOptions := CertificateOptions{
		SigningKey:   ca.PrivateKey,
		IsSelfSigned: false,
		SigningCert:  ca.Certificate,
		Org:          ca.Org,
		CommonName:   cn,
		SANS:         sans,
		IPSANS:       ipsans,
		IsCA:         false,
	}
	return NewCertificate(&certificateOptions)
}
func (ca *CA) NewX509Certificate(cn string, sans, ipsans []string) (*x509.Certificate, crypto.PrivateKey, error) {
	certificateOptions := CertificateOptions{
		SigningKey:   ca.PrivateKey,
		IsSelfSigned: false,
		SigningCert:  ca.Certificate,
		Org:          ca.Org,
		CommonName:   cn,
		SANS:         sans,
		IPSANS:       ipsans,
		IsCA:         false,
	}
	return NewX509Certificate(&certificateOptions)
}
func (ca *CA) NewTLSCertificate(cn string, sans, ipsans []string) (tls.Certificate, error) {
	certificateOptions := CertificateOptions{
		SigningKey:   ca.PrivateKey,
		IsSelfSigned: false,
		SigningCert:  ca.Certificate,
		Org:          ca.Org,
		CommonName:   cn,
		SANS:         sans,
		IPSANS:       ipsans,
		IsCA:         false,
	}
	return NewTLSCertificate(&certificateOptions)
}
