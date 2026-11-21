package user

import (
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrUserPasswordNotSupplied = errors.New("no password supplied for user")
	ErrUsernamePasswordInvalid = errors.New("username and password combination invalid")
	USER_KINDS                 = []string{
		"user",
		"service",
		"agent",
	}
	USER_META_KEYS = []string{
		"email",
		"name",
		"hostId",
	}
)

// For logging activity of admin and user
type Activity struct {
	ID               string    `json:"id"`
	LastIP           net.IP    `json:"last_ip"`
	LastIPv4         string    `json:"last_ipv4"`
	Active           bool      `json:"active"`
	LoginCount       int       `json:"login_count"`
	FailedLoginCount int       `json:"failed_login_count"`
	LastFailedLogin  time.Time `json:"last_failed_login"`
	Locked           bool      `json:"locked"`
	LockedAt         time.Time `json:"locked_at"`
	LoggedIn         bool      `json:"loggedin"`
	LastLogin        time.Time `json:"last_login"`
	LastActivity     time.Time `json:"last_activity"`
}

func (activity *Activity) Update(a Activity) error {
	if a.LastIP != nil {
		activity.LastIP = a.LastIP
	}
	if a.LastIPv4 != "" {
		activity.LastIPv4 = a.LastIPv4
	}
	activity.Active = a.Active
	activity.LoginCount = activity.LoginCount + a.LoginCount
	activity.FailedLoginCount = activity.FailedLoginCount + a.FailedLoginCount
	if !a.LastFailedLogin.IsZero() {
		activity.LastFailedLogin = a.LastFailedLogin
	}
	if a.Locked {
		activity.Locked = true
	}
	if !a.LockedAt.IsZero() {
		activity.LockedAt = a.LockedAt
	}
	activity.LoggedIn = a.LoggedIn
	activity.LastLogin = a.LastLogin
	activity.LastActivity = time.Now()
	return nil
}

// ////////////////////////////////////////////////////////////////////////////////////////
// UserBase
// ////////////////////////////////////////////////////////////////////////////////////////
type UserConfig struct {
	Email     string   `json:"email"`
	FirstName string   `json:"firstname"`
	LastName  string   `json:"lastname"`
	Password  string   `json:"password"`
	Roles     []string `json:"roles"`
	AccountID string   `json:"account_id"`
	Name      string   `json:"name"`
	HostID    string   `json:"host_id"`
	Hostname  string   `json:"hostname"`
	// Permissions restricts the NATS subjects the identity may use. Nil
	// means unrestricted.
	Permissions *NATSPermissions `json:"permissions,omitempty"`
}

// Token here is the original token issued when the user is created.
// Subsequent token requests are not stored in the user object (is up to client).
type UserBase struct {
	ID        string          `json:"id"`
	CreatedAt time.Time       `json:"created_at"`
	Roles     []string        `json:"roles"`
	PublicKey string          `json:"publicKey"`
	Claims    *jwt.UserClaims `json:"claims"`
	Token     string          `json:"token"`
	Seed      string          `json:"seed"`
	AccountID string          `json:"account_id"`
	// Permissions are the NATS subject permissions written into the
	// identity's JWT. Nil means unrestricted.
	Permissions *NATSPermissions `json:"permissions,omitempty"`
}

func (userBase *UserBase) New(signingKeyPair nkeys.KeyPair) error {
	userBase.CreatedAt = time.Now()
	kp, err := nkeys.CreateUser()
	if err != nil {
		return err
	}
	publicKey, err := kp.PublicKey()
	if err != nil {
		return err
	}
	userBase.PublicKey = publicKey
	userBase.Claims = jwt.NewUserClaims(publicKey)
	for _, role := range userBase.Roles {
		userBase.Claims.Tags.Add(role)
	}
	userBase.Permissions.apply(userBase.Claims)
	tokenString, err := userBase.Claims.Encode(signingKeyPair)
	if err != nil {
		return err
	}
	userBase.Token = tokenString
	seed, err := kp.Seed()
	if err != nil {
		return err
	}
	userBase.Seed = string(seed)
	return nil
}

// Reissue rewrites the claims from the current Roles and Permissions and
// signs a new token with the account key, keeping the identity's nkey.
func (userBase *UserBase) Reissue(signingKeyPair nkeys.KeyPair) error {
	if userBase.PublicKey == "" {
		return errors.New("user: cannot reissue a token without a public key")
	}
	claims := jwt.NewUserClaims(userBase.PublicKey)
	for _, role := range userBase.Roles {
		claims.Tags.Add(role)
	}
	userBase.Permissions.apply(claims)
	tokenString, err := claims.Encode(signingKeyPair)
	if err != nil {
		return err
	}
	userBase.Claims = claims
	userBase.Token = tokenString
	return nil
}

// ////////////////////////////////////////////////////////////////////////////////////////
// User
// ////////////////////////////////////////////////////////////////////////////////////////
type User struct {
	UserBase
	Email           string            `json:"email"`
	FirstName       string            `json:"firstname"`
	LastName        string            `json:"lastname"`
	OAuthProvider   string            `json:"oauth_provider,omitempty"`   // OAuth provider name (e.g., "google")
	OAuthSubject    string            `json:"oauth_subject,omitempty"`    // OAuth provider's unique identifier
	Picture         string            `json:"picture,omitempty"`          // Profile picture URL from OAuth
	ProfilePhoto    string            `json:"profile_photo,omitempty"`    // Base64-encoded thumbnail photo (max 128x128)
	ThemePreference string            `json:"theme_preference,omitempty"` // UI theme: "system", "dark", or "light"
	Preferences     map[string]string `json:"preferences,omitempty"`      // Generic key-value user preferences
}

// For roles will use user.Claims.Tags.Add(p ...string)
func NewUser(userConfig *UserConfig, signingKeyPair nkeys.KeyPair) (*User, *UserPassword, error) {
	user := User{
		Email:     userConfig.Email,
		FirstName: userConfig.FirstName,
		LastName:  userConfig.LastName,
	}
	user.ID = fmt.Sprintf("%v:%v", userConfig.AccountID, userConfig.Email)
	user.Roles = userConfig.Roles
	user.AccountID = userConfig.AccountID
	user.Permissions = userConfig.Permissions

	userPassword := UserPassword{}
	if userConfig.Password != "" {
		userPassword.HashCost = 12
		userPassword.PasswordHash = PasswordHashAndSalt(userConfig.Password, userPassword.HashCost)
		userPassword.ID = user.ID
	}
	err := user.New(signingKeyPair)
	if err != nil {
		return &user, &userPassword, err
	}
	return &user, &userPassword, nil
}

// OAuthUserConfig contains configuration for creating an OAuth user
type OAuthUserConfig struct {
	Email         string   `json:"email"`
	FirstName     string   `json:"firstname"`
	LastName      string   `json:"lastname"`
	Roles         []string `json:"roles"`
	AccountID     string   `json:"account_id"`
	OAuthProvider string   `json:"oauth_provider"`
	OAuthSubject  string   `json:"oauth_subject"`
	Picture       string   `json:"picture"`
}

// NewOAuthUser creates a new user from OAuth without password
// OAuth users get NATS nkeys just like local users
func NewOAuthUser(config *OAuthUserConfig, signingKeyPair nkeys.KeyPair) (*User, error) {
	user := User{
		Email:         config.Email,
		FirstName:     config.FirstName,
		LastName:      config.LastName,
		OAuthProvider: config.OAuthProvider,
		OAuthSubject:  config.OAuthSubject,
		Picture:       config.Picture,
	}
	user.ID = fmt.Sprintf("%v:%v", config.AccountID, config.Email)
	user.Roles = config.Roles
	user.AccountID = config.AccountID

	err := user.New(signingKeyPair)
	if err != nil {
		return &user, err
	}
	return &user, nil
}

func (user *User) GetID() string {
	return user.ID
}

// SetRoles replaces the user's roles and re-syncs the role tags carried in the
// user's JWT claims so that a subsequently minted token reflects the new roles.
// Federated (OIDC) login uses this to make the IdP authoritative over roles.
func (user *User) SetRoles(roles []string) {
	user.Roles = roles
	if user.Claims != nil {
		user.Claims.Tags = jwt.TagList{}
		for _, role := range roles {
			user.Claims.Tags.Add(role)
		}
	}
}

func (user *User) CreateToken(tokenValidMinutes int, keyPair nkeys.KeyPair) (Token, error) {
	return NewUserToken(user, tokenValidMinutes, keyPair)
}

func (user *User) NATSCreds(seed string) nats.Option {
	return func(o *nats.Options) error {
		userCB := func() (string, error) {
			return user.Token, nil
		}
		sigCB := func(nonce []byte) ([]byte, error) {
			kp, err := nkeys.FromSeed([]byte(seed))
			if err != nil {
				return nil, err
			}
			sig, _ := kp.Sign(nonce)
			return sig, nil
		}
		o.UserJWT = userCB
		o.SignatureCB = sigCB
		return nil
	}
}

// User password hash and salt.
type UserPassword struct {
	ID           string `json:"id"`
	PasswordHash string `json:"passwordhash"`
	HashCost     int    `json:"hashcost"`
}

// Bcrypt salts the password. Store the cost as well.
// otherwise: returns salt, passwordHash.
func PasswordHashAndSalt(password string, hashCost int) string {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), hashCost)
	if err != nil {
		return ""
	}
	return string(hash)
}

// Passwords should not be sent - will only send bcrypt of password and compared.
func ValidatePassword(hashedPassword, password string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(password))
	if err != nil {
		return false
	}
	return true
}

type Token struct {
	Type    jwt.ClaimType `json:"type"`
	Subject string        `json:"subject"`
	Token   string        `json:"token"`
}

func (token *Token) String() string {
	return token.Token
}

// privateKey is the issuing account
// Leaving privateKey as interface{} today for future changes
func NewUserToken(user *User, minutesValid int, keyPair nkeys.KeyPair) (Token, error) {
	expires := time.Now().Add(time.Minute * time.Duration(minutesValid))
	// This is the public key of the user
	user.Claims.Expires = expires.Unix()
	publicKey, err := keyPair.PublicKey()
	if err != nil {
		return Token{}, err
	}
	user.Claims.IssuerAccount = publicKey
	tokenString, err := user.Claims.Encode(keyPair)
	if err != nil {
		return Token{}, err
	}
	return Token{
		Type:    user.Claims.Type,
		Subject: user.Claims.Subject,
		Token:   tokenString,
	}, nil
}

func ValidateUserToken(tokenString string, publicKey string) (*UserClaims, error) {
	claims, err := DecodeUserClaims(tokenString)
	if err != nil {
		return nil, err
	}
	if claims.Issuer != publicKey {
		return nil, errors.New("jwt issuer does not match")
	}
	// jwt.DecodeUserClaims verifies the signature but not the token lifetime,
	// so enforce expiry and not-before here. Without this a leaked bearer
	// token would remain valid indefinitely until the account key is rotated.
	now := time.Now().Unix()
	if claims.Expires > 0 && now >= claims.Expires {
		return nil, errors.New("jwt is expired")
	}
	if claims.NotBefore > 0 && now < claims.NotBefore {
		return nil, errors.New("jwt is not yet valid")
	}
	return claims, nil
}
