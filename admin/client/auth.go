// Authentication and Authorization Middleware
package client

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/platform/user"
)

type AuthConfig struct {
	BasicAuth       bool
	Store           IdentityStore
	Logger          *logger.Logger
	TrustedAccounts []string
}

// Store is optional when middleware is used by distributed
// service API endpoint - validates JWTs issued by trusted
// accounts. Authorization using JWT claims.
type Auth struct {
	basicAuth       bool
	st              IdentityStore
	l               *logger.Logger
	trustedAccounts []string
	mu              sync.RWMutex
}

func (auth *Auth) IsPublicKeyTrusted(publicKey string) bool {
	auth.mu.RLock()
	defer auth.mu.RUnlock()
	for _, key := range auth.trustedAccounts {
		if publicKey == key {
			return true
		}
	}
	return false
}

// NewAuth returns a *Auth. It returns a pointer (not a value) because Auth
// contains a sync.RWMutex and must not be copied.
func NewAuth(authConfig AuthConfig) *Auth {
	auth := &Auth{}
	auth.trustedAccounts = make([]string, 0)
	auth.basicAuth = authConfig.BasicAuth
	// The store serves Basic auth and the role gates alike, so it is kept
	// whether or not Basic auth is enabled.
	auth.st = authConfig.Store
	auth.trustedAccounts = authConfig.TrustedAccounts
	auth.l = authConfig.Logger
	return auth
}
func (auth *Auth) AddTrustedAccount(id string) {
	auth.mu.Lock()
	defer auth.mu.Unlock()
	auth.trustedAccounts = append(auth.trustedAccounts, id)
}

// SetStore attaches an admin Store handle to the auth middleware so
// RequireRole can look up users by their NATS public key and check
// their roles. Used by services that share a process with the admin
// server (the media server, for example) and need to gate handlers by
// the user's role on their own tenant account, not just by whether
// the caller is in the super-admin ADMIN account.
func (auth *Auth) SetStore(st IdentityStore) {
	auth.mu.Lock()
	defer auth.mu.Unlock()
	auth.st = st
}

type AuthContextKey struct {
	Key string
}

var (
	authContextNoAuthKey         AuthContextKey = AuthContextKey{Key: "noauth"}
	authContextValidTokenKey     AuthContextKey = AuthContextKey{Key: "validtoken"}
	authContextClaimsKey         AuthContextKey = AuthContextKey{Key: "claims"}
	authContextValidBasicAuthKey AuthContextKey = AuthContextKey{Key: "validbasicauth"}
	unauthorized                                = `{"result":"error", "message": "unauthorized"}`
)

// Basic Auth Only
func (auth *Auth) Basic(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authorizationHeader := r.Header.Get("authorization")
		if authorizationHeader != "" {
			field := strings.Split(authorizationHeader, " ")
			if len(field) == 2 {
				// Handle authentication schemes
				// https://developer.mozilla.org/en-US/docs/Web/HTTP/Authentication
				// Bearer
				// Basic (username/password)
				scheme := field[0]
				credential := field[1]
				switch scheme {
				case "Bearer":
					auth.l.Info("authorization failed: basic auth only supported on this endpoint")
					http.Error(w, unauthorized, http.StatusUnauthorized)
				case "Basic":
					if auth.basicAuth {
						b, err := base64.StdEncoding.DecodeString(credential)
						if err != nil {
							auth.l.InfoE().Err(err).Msg("authorization failed: error base64 decoding user/password from auth header")
							http.Error(w, unauthorized, http.StatusUnauthorized)
							//return "", "", err
						}
						pair := strings.SplitN(string(b), ":", 2)
						if len(pair) != 2 {
							auth.l.InfoE().Err(err).Msg("authorization failed: error splitting user/password pair from base64 decoded auth header")
							http.Error(w, unauthorized, http.StatusUnauthorized)
						}
						// Check if account is locked before validating password
						if existingActivity, actErr := auth.st.GetActivity(AccountIDFromContext(r.Context()), UserIDFromContext(r.Context())); actErr == nil && existingActivity.Locked {
							auth.l.InfoE().Str("userID", UserIDFromContext(r.Context())).Msg("authorization failed: account locked")
							http.Error(w, `{"error":"account locked"}`, http.StatusForbidden)
							return
						}
						valid, err := auth.st.ValidateUserPassword(AccountIDFromContext(r.Context()), UserIDFromContext(r.Context()), pair[1])
						if err != nil {
							auth.l.InfoE().Str("user", pair[0]).Str("userID", UserIDFromContext(r.Context())).Err(err).Msg("authorization failed for user")
							auth.recordFailedLogin(r)
							http.Error(w, unauthorized, http.StatusUnauthorized)

						} else if valid {
							h(w, r.WithContext(context.WithValue(r.Context(), authContextValidBasicAuthKey, true)))
						} else {
							auth.l.InfoE().Str("user", pair[0]).Str("userID", UserIDFromContext(r.Context())).Msg("authorization failed: user and password combination not valid")
							auth.recordFailedLogin(r)
							http.Error(w, unauthorized, http.StatusUnauthorized)
						}
					} else {
						auth.l.Info("basic auth not supported on this endpoint")
						http.Error(w, unauthorized, http.StatusUnauthorized)
					}
				default:
					auth.l.InfoE().Str("scheme", scheme).Msg("authorization failed: authorization scheme not supported")
					http.Error(w, unauthorized, http.StatusUnauthorized)
				}
			} else {
				auth.l.Info("authorization failed: no authorization scheme provided")
				http.Error(w, unauthorized, http.StatusUnauthorized)
			}
		} else {
			h(w, r.WithContext(context.WithValue(r.Context(), authContextNoAuthKey, true)))

		}
	}
}

// Bearer Auth Only
func (auth *Auth) Bearer(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authorizationHeader := r.Header.Get("authorization")
		if authorizationHeader != "" {
			field := strings.Split(authorizationHeader, " ")
			if len(field) == 2 {
				// Handle authentication schemes
				// https://developer.mozilla.org/en-US/docs/Web/HTTP/Authentication
				// Bearer
				// Basic (username/password)
				scheme := field[0]
				credential := field[1]
				switch scheme {
				case "Bearer":
					publicKey := AccountPublicKeyFromContext(r.Context())
					if !auth.IsPublicKeyTrusted(publicKey) {
						auth.l.InfoE().Str("publicKey", publicKey).Strs("trustedAccounts", auth.trustedAccounts).Msg("authorization failed: account public key is not trusted")
						http.Error(w, unauthorized, http.StatusUnauthorized)
						return
					}
					// Here we can look at claims and make authorization decisions.
					if claims, err := user.ValidateUserToken(credential, publicKey); err != nil {
						auth.l.InfoE().Err(err).Msg("authorization failed: error validating token from token string")
						http.Error(w, unauthorized, http.StatusUnauthorized)
						return
					} else {
						em := r.WithContext(context.WithValue(r.Context(), idContextUserID, claims.Subject))
						er := em.WithContext(context.WithValue(em.Context(), authContextValidTokenKey, true))
						h(w, er.WithContext(context.WithValue(er.Context(), authContextClaimsKey, claims)))
					}
				case "Basic":
					auth.l.Info("authorization failed: basic auth not supported on this endpoint")
					http.Error(w, unauthorized, http.StatusUnauthorized)
					return
				default:
					auth.l.InfoE().Str("scheme", scheme).Msg("authorization failed: authorization scheme not supported")
					http.Error(w, unauthorized, http.StatusUnauthorized)
					return
				}
			} else {
				auth.l.Info("authorization failed: no authorization scheme provided")
				http.Error(w, unauthorized, http.StatusUnauthorized)
				return
			}
		} else {
			auth.l.Info("authorization failed: bearer token required but not provided")
			http.Error(w, unauthorized, http.StatusUnauthorized)
			return
		}
	}
}

// Basic -or- Bearer
func (auth *Auth) BasicOrBearer(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authorizationHeader := r.Header.Get("authorization")
		if authorizationHeader != "" {
			field := strings.Split(authorizationHeader, " ")
			if len(field) == 2 {
				// Handle authentication schemes
				// https://developer.mozilla.org/en-US/docs/Web/HTTP/Authentication
				// Bearer
				// Basic (username/password)
				scheme := field[0]
				credential := field[1]
				switch scheme {
				case "Basic":
					if auth.basicAuth {
						b, err := base64.StdEncoding.DecodeString(credential)
						if err != nil {
							auth.l.InfoE().Err(err).Msg("authorization failed: error base64 decoding email/password from auth header")
							http.Error(w, unauthorized, http.StatusUnauthorized)
							return
						}
						pair := strings.SplitN(string(b), ":", 2)
						if len(pair) != 2 {
							auth.l.InfoE().Err(err).Msg("authorization failed: error splitting email/password pair from base64 decoded auth header")
							http.Error(w, unauthorized, http.StatusUnauthorized)
							return
						}
						// Check if account is locked before validating password
						if existingActivity, actErr := auth.st.GetActivity(AccountIDFromContext(r.Context()), UserIDFromContext(r.Context())); actErr == nil && existingActivity.Locked {
							auth.l.InfoE().Str("userID", UserIDFromContext(r.Context())).Msg("authorization failed: account locked")
							http.Error(w, `{"error":"account locked"}`, http.StatusForbidden)
							return
						}
						valid, err := auth.st.ValidateUserPassword(AccountIDFromContext(r.Context()), UserIDFromContext(r.Context()), pair[1])
						if err != nil {
							auth.l.InfoE().Str("email", pair[0]).Str("userID", UserIDFromContext(r.Context())).Err(err).Msg("authorization failed for email and password combination")
							auth.recordFailedLogin(r)
							http.Error(w, unauthorized, http.StatusUnauthorized)
							return
						} else if valid {
							er := r.WithContext(context.WithValue(r.Context(), idContextUserID, pair[0]))
							h(w, er.WithContext(context.WithValue(er.Context(), authContextValidBasicAuthKey, true)))
						} else {
							auth.l.InfoE().Str("email", pair[0]).Msg("authorization failed: email and password combination not valid")
							auth.recordFailedLogin(r)
							http.Error(w, unauthorized, http.StatusUnauthorized)
							return
						}
					} else {
						auth.l.Info("authorization failed: basic mode not supported with endpoint")
						http.Error(w, unauthorized, http.StatusUnauthorized)
						return
					}
				case "Bearer":
					publicKey := AccountPublicKeyFromContext(r.Context())
					if !auth.IsPublicKeyTrusted(publicKey) {
						auth.l.InfoE().Str("publicKey", publicKey).Strs("trustedAccounts", auth.trustedAccounts).Msg("authorization failed: account public key is not trusted")
						http.Error(w, unauthorized, http.StatusUnauthorized)
						return
					}
					if claims, err := user.ValidateUserToken(credential, publicKey); err != nil {
						auth.l.InfoE().Err(err).Msg("authorization failed: error validating token from token string")
						http.Error(w, unauthorized, http.StatusUnauthorized)
						return
					} else {
						// Add user uuid, validtoken=true, and claims to context
						em := r.WithContext(context.WithValue(r.Context(), idContextUserID, claims.Subject))
						er := em.WithContext(context.WithValue(em.Context(), authContextValidTokenKey, true))
						h(w, er.WithContext(context.WithValue(er.Context(), authContextClaimsKey, claims)))
					}
				default:
					auth.l.InfoE().Str("scheme", scheme).Msg("authorization failed: authorization scheme not supported")
					http.Error(w, unauthorized, http.StatusUnauthorized)
					return
				}
			} else {
				auth.l.Info("authorization failed: no authorization scheme provided")
				http.Error(w, unauthorized, http.StatusUnauthorized)
				return
			}
		} else {
			auth.l.Info("authorization failed: bearer token required but not provided")
			http.Error(w, unauthorized, http.StatusUnauthorized)
			return
		}
	}
}

// RequireSuperAdmin wraps a Bearer-authenticated handler and only allows
// users from the ADMIN account. Returns 403 for all other users.
func (auth *Auth) RequireSuperAdmin(adminAccountID string, h http.HandlerFunc) http.HandlerFunc {
	return auth.Bearer(func(w http.ResponseWriter, r *http.Request) {
		accountID := AccountIDFromContext(r.Context())
		if accountID != adminAccountID {
			auth.l.InfoE().Str("accountID", accountID).Msg("authorization failed: super admin required")
			http.Error(w, `{"result":"error", "message": "forbidden"}`, http.StatusForbidden)
			return
		}
		h(w, r)
	})
}

// RequireRole wraps a Bearer-authenticated handler and allows users from the
// ADMIN account (super admins) or users with the specified role in any account.
// Uses user.HasRole which accepts both singular and legacy plural role names.
// Returns 403 for all other users.
func (auth *Auth) RequireRole(adminAccountID string, role string, h http.HandlerFunc) http.HandlerFunc {
	return auth.Bearer(func(w http.ResponseWriter, r *http.Request) {
		accountID := AccountIDFromContext(r.Context())
		if accountID == adminAccountID {
			h(w, r)
			return
		}
		userID := UserIDFromContext(r.Context())
		if auth.st == nil {
			auth.l.Info("authorization failed: store not available for role check")
			http.Error(w, `{"result":"error", "message": "forbidden"}`, http.StatusForbidden)
			return
		}
		// userID from context is the NATS public key (claims.Subject), not the store ID.
		// Look up the user by their public key instead.
		u, err := auth.st.GetUserByPublicKey(accountID, userID)
		if err != nil {
			auth.l.InfoE().Str("userID", userID).Err(err).Msg("authorization failed: error getting user for role check")
			http.Error(w, `{"result":"error", "message": "forbidden"}`, http.StatusForbidden)
			return
		}
		if !user.HasRole(u.Roles, role) {
			auth.l.InfoE().Str("userID", userID).Str("role", role).Msg("authorization failed: user does not have required role")
			http.Error(w, `{"result":"error", "message": "forbidden"}`, http.StatusForbidden)
			return
		}
		h(w, r)
	})
}

// RequireAdmin wraps a Bearer-authenticated handler and allows users from the
// ADMIN account (super admins) or users with the administrator role in any
// account. Returns 403 for all other users.
func (auth *Auth) RequireAdmin(adminAccountID string, h http.HandlerFunc) http.HandlerFunc {
	return auth.RequireRole(adminAccountID, user.RoleAdministrator, h)
}

// Does the request have a valid token?
func ValidTokenFromContext(ctx context.Context) bool {
	// ctx.Value returns nil if ctx has no value for the key;
	// the net.IP type assertion returns ok=false for nil.
	if validtoken, ok := ctx.Value(authContextValidTokenKey).(bool); ok {
		return validtoken
	}
	return false
}

// ClaimsFromContext returns the validated user claims attached by the Bearer
// middleware, or nil if the request was not bearer-authenticated. The claims
// are stored as *user.UserClaims (see Bearer/BasicOrBearer), so this must
// assert that concrete type.
func ClaimsFromContext(ctx context.Context) *user.UserClaims {
	if claims, ok := ctx.Value(authContextClaimsKey).(*user.UserClaims); ok {
		return claims
	}
	return nil
}

// If notoken is attached to the context, there was no token attached to the request.
func NoAuthFromContext(ctx context.Context) bool {
	// ctx.Value returns nil if ctx has no value for the key;
	// the net.IP type assertion returns ok=false for nil.
	if noauth, ok := ctx.Value(authContextNoAuthKey).(bool); ok {
		return noauth
	}
	return true
}

// Is the user valid - from Basis Authentication with email:password.
func ValidBasicAuthFromContext(ctx context.Context) bool {
	// ctx.Value returns nil if ctx has no value for the key;
	// the net.IP type assertion returns ok=false for nil.
	if valid, ok := ctx.Value(authContextValidBasicAuthKey).(bool); ok {
		return valid
	}
	return false
}

// recordFailedLogin updates the activity record for a failed login attempt.
// Requires that the ID middleware has already populated the account and user IDs.
func (auth *Auth) recordFailedLogin(r *http.Request) {
	accountID := AccountIDFromContext(r.Context())
	userID := UserIDFromContext(r.Context())
	if accountID == "" || userID == "" {
		return
	}
	if auth.st == nil {
		return
	}
	failActivity := user.Activity{
		ID:               userID,
		FailedLoginCount: 1,
		LastFailedLogin:  time.Now(),
	}
	err := auth.st.UpdateActivity(accountID, failActivity)
	if err != nil {
		auth.l.InfoE().Str("userID", userID).Err(err).Msg("failed login: error updating activity")
	}
	// Lock account after 5 failed attempts
	updated, err := auth.st.GetActivity(accountID, userID)
	if err == nil && updated.FailedLoginCount >= 5 && !updated.Locked {
		updated.Locked = true
		updated.LockedAt = time.Now()
		auth.st.PutActivity(accountID, updated)
		auth.l.InfoE().Int("failedAttempts", updated.FailedLoginCount).Str("userID", userID).Msg("account locked after failed attempts")
	}
}
