package user

import (
	"crypto/ed25519"
	"crypto/tls"
)

type Credentials struct {
	AccountID string `json:"account_id"`

	// NATS
	JWT  string `json:"jwt"`
	Seed string `json:"seed"`

	// HTTP API
	TrustedAccounts []string `json:"trusted_accounts"`

	// TLS
	PrivateKey  *ed25519.PrivateKey `json:"private_key"`
	Certificate *tls.Certificate    `json:"certificate"`
	CA          *tls.Certificate    `json:"ca"`
}

func (credentials *Credentials) AddTrustedAccount(accountID string) {
	credentials.TrustedAccounts = append(credentials.TrustedAccounts, accountID)
}
