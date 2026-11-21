package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

type PrivateKey []byte

type PublicKey []byte

func NewKey() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
}

func NewMatchingKey(inputKey crypto.PrivateKey) (crypto.PrivateKey, error) {
	switch kt := inputKey.(type) {
	case *rsa.PrivateKey:
		return rsa.GenerateKey(rand.Reader, 2048)
	case *ecdsa.PrivateKey:
		return ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	case ed25519.PrivateKey:
		_, privateKey, err := ed25519.GenerateKey(rand.Reader)
		return privateKey, err
	default:
		return nil, errors.New(fmt.Sprintf("pki: key: do not recognize the public key type: %#v, %#v", inputKey, kt))
	}
}

// ParsePrivateKeyFromDER is derived from the Go standard library (crypto/tls).
//
// Copyright 2009 The Go Authors. All rights reserved.
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions are
// met:
//
//   - Redistributions of source code must retain the above copyright
//     notice, this list of conditions and the following disclaimer.
//   - Redistributions in binary form must reproduce the above
//     copyright notice, this list of conditions and the following disclaimer
//     in the documentation and/or other materials provided with the
//     distribution.
//   - Neither the name of Google LLC nor the names of its
//     contributors may be used to endorse or promote products derived from
//     this software without specific prior written permission.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
// "AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
// LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
// A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
// OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
// SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
// LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
// DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
// THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
// (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
// OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
func ParsePrivateKeyFromDER(derBytes PrivateKey) (crypto.PrivateKey, error) {
	if key, err := x509.ParsePKCS1PrivateKey(derBytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS8PrivateKey(derBytes); err == nil {
		switch key := key.(type) {
		case *rsa.PrivateKey, *ecdsa.PrivateKey, ed25519.PrivateKey:
			return key, nil
		default:
			return nil, errors.New("pki: key: found unknown private key type in PKCS#8 wrapping")
		}
	}
	if key, err := x509.ParseECPrivateKey(derBytes); err == nil {
		return key, nil
	}

	return nil, errors.New("pki: key: failed to parse private key")
}

func ParsePrivateKeyFromPEM(pemBytes []byte) (crypto.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block != nil {
		switch blockType := block.Type; blockType {
		case "PRIVATE KEY":
			return x509.ParsePKCS8PrivateKey(block.Bytes)
		case "RSA PRIVATE KEY":
			return x509.ParsePKCS1PrivateKey(block.Bytes)
		default:
			return nil, errors.New(fmt.Sprintf("pki: key: unable to decode PEM block from input: %v", blockType))
		}
	}
	return nil, errors.New("pki: key: unable to decode pem from input")

}

func PublicKeyFromPrivateKey(privateKey crypto.PrivateKey) crypto.PublicKey {
	switch kt := privateKey.(type) {
	case *rsa.PrivateKey:
		return privateKey.(*rsa.PrivateKey).Public()
	case *ecdsa.PrivateKey:
		return privateKey.(*ecdsa.PrivateKey).Public()
	case ed25519.PrivateKey:
		return privateKey.(ed25519.PrivateKey).Public()
	default:
		fmt.Printf("do not recognize the private key type: %v", kt)
		return nil
	}
}
