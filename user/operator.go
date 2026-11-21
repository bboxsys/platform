package user

import (
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

//////////////////////////////////////////////////////////////////////////////////////////
// Operator
//////////////////////////////////////////////////////////////////////////////////////////

type OperatorConfig struct {
	Name             string `json:"name"`
	AccountServerURL string `json:"account_server_url"`
	ServiceURL       string `json:"server_url"`
}
type Operator struct {
	ID        string              `json:"id"`
	CreatedAt time.Time           `json:"created_at"`
	Name      string              `json:"name"`
	PublicKey string              `json:"public_key"`
	Seed      string              `json:"seed"`
	Claims    *jwt.OperatorClaims `json:"claims"`
	Token     string              `json:"token"`
}

func NewOperator(o OperatorConfig) (Operator, error) {
	operator := Operator{
		Name: o.Name,
	}
	operator.CreatedAt = time.Now()
	kp, err := nkeys.CreateOperator()
	if err != nil {
		return operator, err
	}
	seed, err := kp.Seed()
	if err != nil {
		return operator, err
	}
	operator.Seed = string(seed)
	pk, err := kp.PublicKey()
	if err != nil {
		return operator, err
	}
	operator.Claims = jwt.NewOperatorClaims(pk)
	operator.Claims.AccountServerURL = o.AccountServerURL
	operator.Claims.OperatorServiceURLs.Add(o.ServiceURL)
	operator.ID = operator.Claims.Subject
	publicKey, err := kp.PublicKey()
	if err != nil {
		return operator, err
	}
	operator.PublicKey = publicKey
	token, err := operator.Claims.Encode(kp)
	if err != nil {
		return operator, err
	}
	operator.Token = token
	return operator, nil
}

func (operator *Operator) GetID() string {
	return operator.ID
}

func (operator *Operator) GetSigningKeyPair() nkeys.KeyPair {
	keyPair, _ := nkeys.FromSeed([]byte(operator.Seed))
	return keyPair
}
