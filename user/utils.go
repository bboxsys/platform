package user

import (
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
)

var (
	ErrPrivateKeyNotED25519    = errors.New("tls: private key must by type ed25519")
	ErrPrivateKeyParsingFailed = errors.New("tls: failed to parse private key")
	ErrBadSeedLength           = errors.New("ed25519: bad seed length")
)

// ParsePKCS8PrivateKey is derived from the Go standard library (crypto/tls).
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
func ParsePKCS8PrivateKey(der []byte) (*ed25519.PrivateKey, error) {
	if key, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		switch key := key.(type) {
		case ed25519.PrivateKey:
			return &key, nil
		default:
			return nil, ErrPrivateKeyNotED25519
		}
	}

	return nil, ErrPrivateKeyParsingFailed
}

func ParseBundleFromFile(certFile, keyFile string) (*tls.Certificate, *ed25519.PrivateKey, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, nil, err
	}
	keyPEMBlock, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, nil, err
	}
	key, err := ParsePKCS8PrivateKey(keyPEMBlock)
	if err != nil {
		return nil, nil, err
	}
	return &cert, key, nil
}

func NewPrivateKeyFromSeed(s string) (ed25519.PrivateKey, error) {
	seed := []byte(s)
	if l := len(seed); l != ed25519.SeedSize {
		return nil, ErrBadSeedLength
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
