package client

import "github.com/bboxsys/platform/user"

// IdentityStore is the slice of the admin store that the Auth and ID
// middleware need: password validation and lockout accounting for basic
// auth, and user lookup for role checks. *admin.Store satisfies it. Services
// that only validate bearer tokens issued by trusted accounts do not need
// one at all.
type IdentityStore interface {
	GetUserByEmail(email string) (*user.User, string, error)
	GetUserByPublicKey(accountID, publicKey string) (*user.User, error)
	GetActivity(accountID, activityID string) (user.Activity, error)
	UpdateActivity(accountID string, a user.Activity) error
	PutActivity(accountID string, activity user.Activity) error
	ValidateUserPassword(accountID, userID, password string) (bool, error)
}
