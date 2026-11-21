package user

import (
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

//////////////////////////////////////////////////////////////////////////////////////////
// Account
//////////////////////////////////////////////////////////////////////////////////////////

type AccountConfig struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Owner       string `json:"owner"`
	OperatorID  string `json:"operator_id"`
}
type Account struct {
	ID          string             `json:"id"`
	CreatedAt   time.Time          `json:"created_at"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Owner       string             `json:"owner"`
	Seed        string             `json:"seed"`
	PublicKey   string             `json:"public_key"`
	Token       string             `json:"token"`
	Claims      *jwt.AccountClaims `json:"claims"`
}

// Operator nkeys.KeyPair passed as argument
func NewAccount(a AccountConfig, signingKeyPair nkeys.KeyPair) (Account, error) {
	account := Account{
		Name:        a.Name,
		Description: a.Description,
		Owner:       a.Owner,
	}
	account.CreatedAt = time.Now()
	kp, err := nkeys.CreateAccount()
	if err != nil {
		return account, err
	}
	seed, err := kp.Seed()
	if err != nil {
		return account, err
	}
	publicKey, err := kp.PublicKey()
	if err != nil {
		return account, err
	}
	account.Seed = string(seed)
	account.Claims = jwt.NewAccountClaims(publicKey)
	account.ID = account.Claims.Subject
	account.PublicKey = publicKey
	tokenString, err := account.Claims.Encode(signingKeyPair)
	if err != nil {
		return account, err
	}
	account.Token = tokenString
	return account, nil
}
func (account *Account) GetID() string {
	return account.ID
}
func (account *Account) GetSigningKeyPair() nkeys.KeyPair {
	keyPair, err := nkeys.FromSeed([]byte(account.Seed))
	if err != nil {
		return nil
	}
	return keyPair
}
