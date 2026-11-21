package admin

import "github.com/bboxsys/platform/admin/client"

// The middleware, context helpers, tenant notifications, and service
// endpoint types live in the client package so that services which only
// validate tokens do not import the admin server. They are re-exported here
// so code written against the original single package keeps compiling.

type (
	Auth                      = client.Auth
	AuthConfig                = client.AuthConfig
	AuthContextKey            = client.AuthContextKey
	ID                        = client.ID
	IDConfig                  = client.IDConfig
	IDContextKey              = client.IDContextKey
	IdentityStore             = client.IdentityStore
	ServiceEndpoint           = client.ServiceEndpoint
	ServiceEndpoints          = client.ServiceEndpoints
	EndpointHealth            = client.EndpointHealth
	TenantCreatedNotification = client.TenantCreatedNotification
	TenantUpdatedNotification = client.TenantUpdatedNotification
	TenantDeletedNotification = client.TenantDeletedNotification
)

const (
	SubjectTenantCreated = client.SubjectTenantCreated
	SubjectTenantUpdated = client.SubjectTenantUpdated
	SubjectTenantDeleted = client.SubjectTenantDeleted
)

var (
	NewAuth                     = client.NewAuth
	NewID                       = client.NewID
	AccountIDFromContext        = client.AccountIDFromContext
	UserIDFromContext           = client.UserIDFromContext
	AccountPublicKeyFromContext = client.AccountPublicKeyFromContext
	ValidTokenFromContext       = client.ValidTokenFromContext
	ClaimsFromContext           = client.ClaimsFromContext
	NoAuthFromContext           = client.NoAuthFromContext
	ValidBasicAuthFromContext   = client.ValidBasicAuthFromContext
	EndpointKinds               = client.EndpointKinds
	InKind                      = client.InKind
	NewServiceEndpoint          = client.NewServiceEndpoint
	ENDPOINT_KINDS              = client.ENDPOINT_KINDS
)
