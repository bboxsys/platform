package user

import (
	"fmt"
	"github.com/nats-io/nkeys"
	"testing"
)

var (
	defaultPassword = "testing123"
	testUserConfigs = []UserConfig{
		{
			FirstName: "Joe",
			LastName:  "Dirt",
			Password:  defaultPassword,
		},
		{
			FirstName: "Mary",
			LastName:  "Jane",
			Password:  defaultPassword,
		},
		{
			FirstName: "Momma",
			LastName:  "Rey",
			Password:  defaultPassword,
		},
	}
	testUsers         = make(map[string]User)
	testUserPasswords = make(map[string]UserPassword)
)

func TestNewUser(t *testing.T) {
	accountKeyPair, err := nkeys.CreateAccount()
	if err != nil {
		t.Errorf("error creating acccount KeyPair: %v", err)
	}
	for _, u := range testUserConfigs {
		user, userPassword, err := NewUser(&u, accountKeyPair)
		if err != nil {
			t.Errorf("error creating new user: %v", err)
		} else {
			testUsers[user.ID] = *user
			testUserPasswords[user.ID] = *userPassword
		}
	}
}

func TestValidatePassword(t *testing.T) {
	badPassword := "badPassword"
	for _, user := range testUsers {
		bad := ValidatePassword(testUserPasswords[user.ID].PasswordHash, badPassword)
		good := ValidatePassword(testUserPasswords[user.ID].PasswordHash, defaultPassword)
		if good && !bad {
			fmt.Printf("password valid...\n")
		} else {
			t.Errorf("password checking failed...\n")
		}
	}
}
