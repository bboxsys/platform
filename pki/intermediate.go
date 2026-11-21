package pki

import (
	"crypto"
	"crypto/tls"
	"crypto/x509"
)

type Intermediate struct {
	CA          *x509.Certificate
	Certificate *x509.Certificate
	PrivateKey  crypto.PrivateKey
	Org         string
}

func NewIntermediate(caFile, certFile, keyFile string, org string) (*Intermediate, error) {
	caCertificate, err := X509CertificateFromFile(caFile)
	if err != nil {
		return &Intermediate{}, err
	}
	certificate, privateKey, err := PairFromFile(certFile, keyFile)
	if err != nil {
		return &Intermediate{}, err
	}
	return &Intermediate{
		CA:          caCertificate,
		Certificate: certificate,
		PrivateKey:  privateKey,
		Org:         org,
	}, nil
}

func (intermediate *Intermediate) Chain() (Certificate, error) {
	caPEM, err := ParseCertificateX509ToPEM(intermediate.CA)
	if err != nil {
		return Certificate{}, err
	}
	intermediatePEM, err := ParseCertificateX509ToPEM(intermediate.Certificate)
	if err != nil {
		return Certificate{}, err
	}
	var returnBytes Certificate
	returnBytes.Append(caPEM, intermediatePEM)
	return returnBytes, nil
}

func (intermediate *Intermediate) NewCertificate(cn string, sans, ipsans []string) (Certificate, PrivateKey, error) {
	certificateOptions := CertificateOptions{
		SigningKey:   intermediate.PrivateKey,
		SigningCert:  intermediate.Certificate,
		Org:          intermediate.Org,
		CommonName:   cn,
		SANS:         sans,
		IPSANS:       ipsans,
		IsCA:         false,
		IsSelfSigned: false,
	}
	return NewCertificateAndPrivateKeyPEM(&certificateOptions)
}

func (intermediate *Intermediate) NewX509Certificate(cn string, sans, ipsans []string) (*x509.Certificate, crypto.PrivateKey, error) {
	certificateOptions := CertificateOptions{
		SigningKey:   intermediate.PrivateKey,
		SigningCert:  intermediate.Certificate,
		Org:          intermediate.Org,
		CommonName:   cn,
		SANS:         sans,
		IPSANS:       ipsans,
		IsCA:         false,
		IsSelfSigned: false,
	}
	return NewX509Certificate(&certificateOptions)
}

func (intermediate *Intermediate) NewTLSCertificate(cn string, sans, ipsans []string) (tls.Certificate, error) {
	certificateOptions := CertificateOptions{
		SigningKey:   intermediate.PrivateKey,
		SigningCert:  intermediate.Certificate,
		Org:          intermediate.Org,
		CommonName:   cn,
		SANS:         sans,
		IPSANS:       ipsans,
		IsCA:         false,
		IsSelfSigned: false,
	}
	return NewTLSCertificate(&certificateOptions)
}
