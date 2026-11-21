// Middleware for IDs
package client

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/platform/user"
	"github.com/gorilla/mux"
)

type IDConfig struct {
	BasicAuth bool
	Store     IdentityStore
	Logger    *logger.Logger
}

type ID struct {
	basicAuth bool
	st        IdentityStore
	l         *logger.Logger
}

func NewID(idConfig IDConfig) ID {
	var id ID
	if idConfig.BasicAuth {
		id.basicAuth = true
		id.st = idConfig.Store
	} else {
		id.basicAuth = false
	}
	id.l = idConfig.Logger
	return id
}

type IDContextKey struct {
	Key string
}

var (
	idContextUserID           IDContextKey = IDContextKey{Key: "userID"}
	idContextAccountID        IDContextKey = IDContextKey{Key: "accountID"}
	idContextAccountPublicKey IDContextKey = IDContextKey{Key: "accountPublicKey"}
)

// Prefer variables in headers "X-Blackbox-{}". Next look in query parameters.
func (id *ID) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		q := r.URL.Query()
		userID := ""
		accountID := ""
		accountPublicKey := ""
		accountIDHeader := r.Header.Get("X-Blackbox-AccountID")
		if accountIDHeader != "" {
			accountID = accountIDHeader
		} else if vars["accountID"] != "" {
			accountID = vars["accountID"]
		} else if q.Get("accountID") != "" {
			accountID = q.Get("accountID")
		}
		accountPublicKey = accountID
		userIDHeader := r.Header.Get("X-Blackbox-UserID")
		if userIDHeader != "" {
			userID = userIDHeader
		} else if vars["userID"] != "" {
			userID = vars["userID"]
		} else if q.Get("userID") != "" {
			userID = q.Get("userID")
		} else {
			authorizationHeader := r.Header.Get("authorization")
			if authorizationHeader != "" {
				field := strings.Split(authorizationHeader, " ")
				if len(field) == 2 {
					scheme := field[0]
					credential := field[1]
					switch scheme {
					case "Bearer":
						if claims, err := user.ValidateUserToken(credential, accountPublicKey); err != nil {
							id.l.InfoE().Err(err).Msg("unable to get id from token")
						} else {
							userID = claims.Subject
						}
					case "Basic":
						if id.basicAuth {
							b, err := base64.StdEncoding.DecodeString(credential)
							if err != nil {
								id.l.InfoE().Err(err).Msg("id failed: error base64 decoding email/password from auth header")
							}
							pair := strings.SplitN(string(b), ":", 2)
							switch pair[0] {
							default:
								u, acctID, err := id.st.GetUserByEmail(pair[0])
								if err != nil {
									id.l.InfoE().Str("email", pair[0]).Err(err).Msg("id failed: error getting user id via email")
									break
								}
								userID = u.ID
								if accountID == "" {
									accountID = acctID
								}
							}
						} else {
							// Basic authentication not supported when running distributed API endpoints.
						}
					}
				}
			}
		}
		ea := r.WithContext(context.WithValue(r.Context(), idContextUserID, userID))
		eb := r.WithContext(context.WithValue(ea.Context(), idContextAccountID, accountID))
		next.ServeHTTP(w, eb.WithContext(context.WithValue(eb.Context(), idContextAccountPublicKey, accountPublicKey)))
	})
}

// Account ID
func AccountIDFromContext(ctx context.Context) string {
	// ctx.Value returns nil if ctx has no value for the key;
	if accountID, ok := ctx.Value(idContextAccountID).(string); ok {
		return accountID
	}
	return ""
}

// User ID
func UserIDFromContext(ctx context.Context) string {
	// ctx.Value returns nil if ctx has no value for the key;
	if userID, ok := ctx.Value(idContextUserID).(string); ok {
		return userID
	}
	return ""
}
func AccountPublicKeyFromContext(ctx context.Context) string {
	// ctx.Value returns nil if ctx has no value for the key;
	if accountPublicKey, ok := ctx.Value(idContextAccountPublicKey).(string); ok {
		return accountPublicKey
	}
	return ""
}
