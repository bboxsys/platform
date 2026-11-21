package user

import (
	"fmt"
	"testing"

	"github.com/nats-io/nkeys"
)

var (
	user           = User{}
	token          = Token{}
	accountKeyPair nkeys.KeyPair
)

func TestNewUserToken(t *testing.T) {
	var err error
	accountKeyPair, err = nkeys.CreateAccount()
	if err != nil {
		t.Errorf("error creating account keyPair: %v", err)
	}
	u, _, _ := NewUser(&UserConfig{
		FirstName: "john",
		LastName:  "doe",
		Email:     "john.doe@user.com",
		Password:  "pass123",
	}, accountKeyPair)
	u.Roles = []string{"admin", "user"}
	token, err = NewUserToken(u, 60, accountKeyPair)
	if err != nil {
		t.Errorf("error creating new token: %v", err)
	}
	fmt.Printf("successfully created token: %#v\n", token)
}

func TestValidateUserToken(t *testing.T) {
	// Validate positive case
	pk, err := accountKeyPair.PublicKey()
	if err != nil {
		t.Errorf("error getting account publicKey: %v", err)
	}
	_, err = ValidateUserToken(token.String(), pk)
	if err == nil {
		fmt.Println("token is valid")
	} else {
		t.Errorf("error validating token")
	}
	// Validate negative case
	invalidAccountKeyPair, err := nkeys.CreateAccount()
	if err != nil {
		t.Errorf("error creating invalid account keyPair: %v", err)
	}
	invalidPK, err := invalidAccountKeyPair.PublicKey()
	if err != nil {
		t.Errorf("error getting invalid account publicKey: %v", err)
	}
	_, err = ValidateUserToken(token.String(), invalidPK)
	if err != nil {
		fmt.Printf("negative token validation case succeeded: %v\n", err)
	} else {
		t.Errorf("token validation negative case succeeded")
	}
}
