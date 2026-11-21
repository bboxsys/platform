package user

import (
	"github.com/nats-io/jwt/v2"
)

// NATSPermissions restricts what a NATS identity (user, agent, or service)
// may publish and subscribe to. The NATS server enforces them from the
// user JWT. A nil or empty value leaves the identity unrestricted, as every
// identity issued before permissions existed is.
//
// Once an allow list is set, everything not listed is denied, so a
// restricted identity must still be allowed the subjects every agent and
// service relies on; see BaselinePublishSubjects.
type NATSPermissions struct {
	PublishAllow   []string `json:"publish_allow,omitempty"`
	PublishDeny    []string `json:"publish_deny,omitempty"`
	SubscribeAllow []string `json:"subscribe_allow,omitempty"`
	SubscribeDeny  []string `json:"subscribe_deny,omitempty"`
}

// IsEmpty reports whether the permissions restrict nothing.
func (p *NATSPermissions) IsEmpty() bool {
	return p == nil || (len(p.PublishAllow) == 0 && len(p.PublishDeny) == 0 &&
		len(p.SubscribeAllow) == 0 && len(p.SubscribeDeny) == 0)
}

// apply copies the permissions onto the claims. Empty permissions leave the
// claims untouched.
func (p *NATSPermissions) apply(claims *jwt.UserClaims) {
	if p.IsEmpty() || claims == nil {
		return
	}
	claims.Permissions.Pub.Allow = append(jwt.StringList(nil), p.PublishAllow...)
	claims.Permissions.Pub.Deny = append(jwt.StringList(nil), p.PublishDeny...)
	claims.Permissions.Sub.Allow = append(jwt.StringList(nil), p.SubscribeAllow...)
	claims.Permissions.Sub.Deny = append(jwt.StringList(nil), p.SubscribeDeny...)
}

// PermissionsFromClaims reads the permissions back out of a user JWT's
// claims; nil when the claims restrict nothing.
func PermissionsFromClaims(claims *jwt.UserClaims) *NATSPermissions {
	if claims == nil {
		return nil
	}
	p := &NATSPermissions{
		PublishAllow:   append([]string(nil), claims.Permissions.Pub.Allow...),
		PublishDeny:    append([]string(nil), claims.Permissions.Pub.Deny...),
		SubscribeAllow: append([]string(nil), claims.Permissions.Sub.Allow...),
		SubscribeDeny:  append([]string(nil), claims.Permissions.Sub.Deny...),
	}
	if p.IsEmpty() {
		return nil
	}
	return p
}

// APIMethods are the HTTP methods a service answers over NATS, in the
// subject scheme api.{service}.{METHOD}.{resource} of package natsapi.
// Kept here so the user package stays free of that import.
var APIMethods = []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD"}

// APIPublishSubjects returns the publish subjects that let an identity call
// the named service's API over NATS with the given methods (all methods
// when none are given): api.{service}.{METHOD}.>.
func APIPublishSubjects(service string, methods ...string) []string {
	if len(methods) == 0 {
		methods = APIMethods
	}
	subjects := make([]string, 0, len(methods))
	for _, m := range methods {
		subjects = append(subjects, "api."+service+"."+m+".>")
	}
	return subjects
}

// BaselinePublishSubjects returns the subjects every agent or service must
// still be allowed to publish once a publish allow list exists: request
// replies and body pulls (_INBOX.>), service discovery ($SRV.>), and its
// own logs, store metrics, and AI usage (sys.log.>, sys.metrics.>,
// sys.ai.>; the prefixes of packages logentry, storemetrics, and ai/usage).
func BaselinePublishSubjects() []string {
	return []string{"_INBOX.>", "$SRV.>", "sys.log.>", "sys.metrics.>", "sys.ai.>"}
}

// ReadOnlyAPIPermissions returns permissions that let an identity read
// (GET and HEAD) the named services over NATS and nothing else, on top of
// the baseline subjects.
func ReadOnlyAPIPermissions(services ...string) *NATSPermissions {
	allow := BaselinePublishSubjects()
	for _, s := range services {
		allow = append(allow, APIPublishSubjects(s, "GET", "HEAD")...)
	}
	return &NATSPermissions{PublishAllow: allow}
}
