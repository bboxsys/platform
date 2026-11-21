package pki

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"time"
)

// this is an x509 certificate in der format(without pem headers).
type Certificate []byte

func (certificate *Certificate) Append(certs ...Certificate) {
	for _, cert := range certs {
		*certificate = append(*certificate, cert...)
	}
}

func NewSelfSignedCertificatePair(name string, org string) ([]byte, []byte, error) {
	return NewSelfSignedCertificatePairWithOptions(name, org, []string{name}, []string{"127.0.0.1"}, time.Time{})
}

// NewSelfSignedCertificatePairWithOptions creates a self-signed certificate pair with explicit SANs,
// IP SANs, and optional expiry. If notAfter is zero, defaults to 1 year.
func NewSelfSignedCertificatePairWithOptions(cn, org string, sans, ipsans []string, notAfter time.Time) ([]byte, []byte, error) {
	certificateOptions := CertificateOptions{
		CommonName:   cn,
		Org:          org,
		SANS:         sans,
		IPSANS:       ipsans,
		IsCA:         true,
		IsSelfSigned: true,
		NotAfter:     notAfter,
	}
	return NewCertificate(&certificateOptions)
}

type CertificateOptions struct {
	SigningKey   crypto.PrivateKey
	SigningCert  *x509.Certificate
	Org          string
	CommonName   string
	SANS         []string
	IPSANS       []string
	IsCA         bool
	IsSelfSigned bool
	NotAfter     time.Time
}

func (options *CertificateOptions) ParseIPSANS() []net.IP {
	ips := make([]net.IP, 0)
	for _, ip := range options.IPSANS {
		ips = append(ips, net.ParseIP(ip))
	}
	return ips
}

func NewTLSCertificate(options *CertificateOptions) (tls.Certificate, error) {
	var certBuffer bytes.Buffer
	var keyBuffer bytes.Buffer
	certBytes, keyBytes, err := NewCertificate(options)
	if err != nil {
		return tls.Certificate{}, err
	}
	err = pem.Encode(&certBuffer, &pem.Block{Type: "CERTIFICATE", Bytes: certBytes})
	if err != nil {
		return tls.Certificate{}, err
	}
	err = pem.Encode(&keyBuffer, &pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(certBuffer.Bytes(), keyBuffer.Bytes())

}
func NewX509Certificate(options *CertificateOptions) (*x509.Certificate, crypto.PrivateKey, error) {
	certBytes, keyBytes, err := NewCertificate(options)
	if err != nil {
		return &x509.Certificate{}, nil, err
	}
	certificate, err := x509.ParseCertificate(certBytes)
	if err != nil {
		return &x509.Certificate{}, nil, err
	}
	privateKey, err := ParsePrivateKeyFromDER(keyBytes)
	if err != nil {
		return &x509.Certificate{}, nil, err
	}
	return certificate, privateKey, nil

}

// This is a PEM enclosed
func NewCertificate(options *CertificateOptions) (Certificate, PrivateKey, error) {
	var privateKey crypto.PrivateKey
	var publicKey crypto.PublicKey
	var err error
	// make sure our new certificate privateKey type matches the signer
	if options.IsSelfSigned {
		privateKey, err = NewKey()
		if err != nil {
			return nil, nil, err
		}
	} else {
		privateKey, err = NewMatchingKey(options.SigningKey)
		if err != nil {
			return nil, nil, err
		}
	}
	publicKey = PublicKeyFromPrivateKey(privateKey)
	notBefore := time.Now()
	var notAfter time.Time
	if options.NotAfter.IsZero() {
		// One year for Catalina
		notAfter = notBefore.AddDate(1, 0, 0)
	} else {
		notAfter = options.NotAfter
	}
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, nil, err
	}
	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{options.Org},
			CommonName:   options.CommonName,
		},
		NotBefore: notBefore,
		NotAfter:  notAfter,
		IsCA:      options.IsCA,
		// Key Usage
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		// Subject Alternative Names
		DNSNames:              options.SANS,
		IPAddresses:           options.ParseIPSANS(),
		BasicConstraintsValid: true,
	}
	var signingKey crypto.PrivateKey
	signingCert := &x509.Certificate{}
	if options.IsSelfSigned {
		signingKey = privateKey
		signingCert = &template
	} else {
		signingKey = options.SigningKey
		signingCert = options.SigningCert
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, signingCert, publicKey, signingKey)
	if err != nil {
		return nil, nil, err
	}
	privBytes, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, nil, err
	}
	return derBytes, privBytes, nil
}

func NewCertificateAndPrivateKeyPEM(options *CertificateOptions) (Certificate, PrivateKey, error) {
	certBytes, keyBytes, err := NewCertificate(options)
	if err != nil {
		return Certificate{}, PrivateKey{}, err
	}
	var certBuffer bytes.Buffer
	var keyBuffer bytes.Buffer
	err = pem.Encode(&certBuffer, &pem.Block{Type: "CERTIFICATE", Bytes: certBytes})
	if err != nil {
		return Certificate{}, PrivateKey{}, err
	}
	err = pem.Encode(&keyBuffer, &pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})
	if err != nil {
		return Certificate{}, PrivateKey{}, err
	}
	return certBuffer.Bytes(), keyBuffer.Bytes(), nil
}

func ParseCertificatePEM(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block != nil {
		switch blockType := block.Type; blockType {
		case "CERTIFICATE":
			return x509.ParseCertificate(block.Bytes)
		default:
			return nil, errors.New(fmt.Sprintf("pki: certificate: unknown block type: %v", blockType))
		}
	}
	return nil, errors.New("pki: certificate: unable to decode pem block from input")

}
func ParseCertificatePEMToDER(pemBytes []byte) (Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block != nil {
		switch blockType := block.Type; blockType {
		case "CERTIFICATE":
			return block.Bytes, nil
		default:
			return nil, errors.New(fmt.Sprintf("pki: certificate: unknown block type: %v", blockType))
		}
	}
	return nil, errors.New("pki: certificate: unable to decode der from pem")

}
func ParseCertificateX509ToPEM(certificate *x509.Certificate) (Certificate, error) {
	var certBuffer bytes.Buffer
	err := pem.Encode(&certBuffer, &pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	if err != nil {
		return Certificate{}, err
	}
	return certBuffer.Bytes(), nil
}
