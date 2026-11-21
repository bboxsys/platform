package user

import (
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

type UserClaims struct {
	jwt.UserClaims
	User  string   `json:"user"`
	Roles []string `json:"roles"`
}

// The subject is a public nkey
func NewUserClaims(user *User) *UserClaims {
	c := &UserClaims{}
	c.Subject = user.PublicKey
	c.User = user.GetID()
	c.Roles = user.Roles

	return c
}

// Encode tries to turn the user claims into a JWT string
func (u *UserClaims) Encode(pair nkeys.KeyPair) (string, error) {
	return u.UserClaims.Encode(pair)
}

// DecodeUserClaims tries to parse a user claims from a JWT string
func DecodeUserClaims(tokenString string) (*UserClaims, error) {
	uc, err := jwt.DecodeUserClaims(tokenString)
	if err != nil {
		return &UserClaims{}, err
	}
	return &UserClaims{*uc, "", nil}, nil
}

// Validate checks the generic and specific parts of the user jwt
func (u *UserClaims) Validate(vr *jwt.ValidationResults) {
	u.UserClaims.Validate(vr)
}

// ExpectedPrefixes defines the types that can encode a user JWT, account
func (u *UserClaims) ExpectedPrefixes() []nkeys.PrefixByte {
	return []nkeys.PrefixByte{nkeys.PrefixByteAccount}
}

// Payload returns the user specific data from a user JWT
func (u *UserClaims) Payload() interface{} {
	return &u.User
}
