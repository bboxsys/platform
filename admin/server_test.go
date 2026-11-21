package admin

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/bboxsys/platform/server"
	"github.com/bboxsys/platform/user"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testAdminEmail    = "admin@test.local"
	testAdminPassword = "testing123"
)

// selfSignedCert returns PEM-encoded key and certificate bytes so
// server.NewServer can build its TLS config without touching disk.
func selfSignedCert(t *testing.T) (keyPEM, certPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return keyPEM, certPEM
}

func newTestAdminServer(t *testing.T, storeLocation string) *AdminServer {
	t.Helper()
	keyPEM, certPEM := selfSignedCert(t)
	adminServer, err := NewAdminServer(&server.ServerOptions{
		Application: "unit_testing",
		Component:   "admin_server",
		PrivateKey:  keyPEM,
		Certificate: certPEM,
		CA:          certPEM,
		HTTPPort:    0,
		Messenger:   false,
		Metadata: map[string]interface{}{
			"storeLocation":        storeLocation,
			"tokenValidMinutes":    60,
			"accountServerURL":     "https://localhost:0",
			"operatorServiceURL":   "https://localhost:0",
			"defaultAdminEmail":    testAdminEmail,
			"defaultAdminPassword": testAdminPassword,
		},
	})
	require.NoError(t, err)
	return adminServer
}

func TestAdminServerBootstrap(t *testing.T) {
	storeLocation := t.TempDir() + "/admin.db"
	adminServer := newTestAdminServer(t, storeLocation)

	adminAccountID := adminServer.AdminAccountID()
	operatorID := adminServer.OperatorID()
	require.NotEmpty(t, adminAccountID, "first run must create the ADMIN account")
	require.NotEmpty(t, operatorID, "first run must create the operator")

	adminAccount, err := adminServer.Store().GetAccountByName("ADMIN")
	require.NoError(t, err)
	assert.Equal(t, adminAccountID, adminAccount.ID)

	sysAccountID, err := adminServer.GetSysAccountID()
	require.NoError(t, err)
	assert.NotEmpty(t, sysAccountID)
	assert.NotEqual(t, adminAccountID, sysAccountID)

	operatorJWT, err := adminServer.GetOperatorJWT()
	require.NoError(t, err)
	assert.NotEmpty(t, operatorJWT)

	// Default admin user exists in the ADMIN account with a working password.
	userID, accountID, err := adminServer.GetUserIDByEmail(testAdminEmail)
	require.NoError(t, err)
	assert.Equal(t, adminAccountID, accountID)
	valid, err := adminServer.Store().ValidateUserPassword(adminAccountID, userID, testAdminPassword)
	require.NoError(t, err)
	assert.True(t, valid)
	valid, err = adminServer.Store().ValidateUserPassword(adminAccountID, userID, "not-the-password")
	require.NoError(t, err)
	assert.False(t, valid)

	// Service bootstrapping: get-or-create the account and mint a NATS user
	// JWT signed by it.
	creds, err := adminServer.ServiceCredentials("queue", "server")
	require.NoError(t, err)
	assert.NotEmpty(t, creds.AccountID)
	assert.NotEmpty(t, creds.JWT)
	assert.NotEmpty(t, creds.Seed)
	assert.Contains(t, creds.TrustedAccounts, creds.AccountID)
	assert.Contains(t, creds.TrustedAccounts, adminAccountID)

	claims, err := user.ValidateUserToken(creds.JWT, creds.AccountID)
	require.NoError(t, err, "service JWT must validate against its account public key")
	assert.NotEmpty(t, claims.Subject)

	// Idempotent: a second call reuses the account and service.
	credsAgain, err := adminServer.ServiceCredentials("queue", "server")
	require.NoError(t, err)
	assert.Equal(t, creds.AccountID, credsAgain.AccountID)

	// Reopening the same store must restore identity instead of re-initializing.
	adminServer.Close()
	reopened := newTestAdminServer(t, storeLocation)
	defer reopened.Close()
	assert.Equal(t, adminAccountID, reopened.AdminAccountID())
	assert.Equal(t, operatorID, reopened.OperatorID())
}
