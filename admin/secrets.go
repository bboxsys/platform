package admin

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// secretsKeyFileName is created next to the admin store (in the parent of the
// store location) on first run when no key is supplied. It is 0600 and holds
// the base64-encoded 32-byte AES key that seals stored secrets.
const secretsKeyFileName = "secrets.key"

// secretsKeyLength is the AES-256 key size.
const secretsKeyLength = 32

// sealedPrefix marks a value that secretBox produced. Values without it are
// legacy plaintext and are returned as-is by open, so records written before
// encryption existed keep working and are re-sealed on their next write.
const sealedPrefix = "enc1:"

var errSecretsKeyLength = fmt.Errorf("secrets key must be %d bytes", secretsKeyLength)

// secretBox seals and opens short secrets with AES-256-GCM.
type secretBox struct {
	aead cipher.AEAD
}

func newSecretBox(key []byte) (*secretBox, error) {
	if len(key) != secretsKeyLength {
		return nil, errSecretsKeyLength
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &secretBox{aead: aead}, nil
}

// seal encrypts plain. An empty string stays empty so "no secret" survives
// a round trip as "no secret".
func (b *secretBox) seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plain), nil)
	return sealedPrefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// open decrypts a value produced by seal. Values without the sealed prefix
// are legacy plaintext and are returned unchanged.
func (b *secretBox) open(value string) (string, error) {
	if !strings.HasPrefix(value, sealedPrefix) {
		return value, nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, sealedPrefix))
	if err != nil {
		return "", err
	}
	nonceSize := b.aead.NonceSize()
	if len(raw) < nonceSize {
		return "", errors.New("sealed secret is too short")
	}
	plain, err := b.aead.Open(nil, raw[:nonceSize], raw[nonceSize:], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// loadOrCreateSecretsKey reads the base64 key at path, generating and writing
// one (0600) if the file does not exist.
func loadOrCreateSecretsKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, fmt.Errorf("secrets key %s: %w", path, err)
		}
		if len(key) != secretsKeyLength {
			return nil, fmt.Errorf("secrets key %s: %w", path, errSecretsKeyLength)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, secretsKeyLength)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	encoded := base64.StdEncoding.EncodeToString(key) + "\n"
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// defaultSecretsKeyPath puts the key file beside the store directory.
func defaultSecretsKeyPath(storeLocation string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(storeLocation)), secretsKeyFileName)
}
