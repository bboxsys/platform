// Package natsapi carries a service's HTTP API over NATS request/reply.
//
// A caller that knows only the NATS controller and its own credentials can
// make the same calls it would make over HTTPS: the same verbs, paths,
// headers, payloads, and status codes. On the service side the request is
// turned into an *http.Request and dispatched through the service's normal
// router, so the ID middleware and every Bearer/RequireAdmin wrapper run
// unchanged and permissions cannot drift from the REST API.
//
// # Wire format
//
// Each request is one NATS message on
//
//	api.{service}.{METHOD}.{resource}
//
// where resource is the first path segment after /api/v1/ (see
// ResourceToken). The headers carry the HTTP headers plus X-Path, the full
// request path and query, which is authoritative; the resource token exists
// only so a user JWT can restrict a caller to, say, api.admin_service.GET.>.
// The service rejects a request whose token does not match its path.
//
// Bodies and responses stream in chunks so they are not bounded by the
// NATS max payload. A response is a sequence of messages on the reply
// inbox: the first carries X-Status and the response headers, every
// message carries X-Seq, and the last carries X-End. A request body larger
// than one chunk is pulled by the service: the caller publishes the first
// message with X-Body-Stream naming a subject it serves, and the service
// requests successive chunks from it until one arrives with X-End.
//
// Service discovery uses the nats micro protocol: every served account
// answers $SRV.PING, $SRV.INFO and $SRV.STATS with the service name and one
// endpoint per verb.
package natsapi

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	// SubjectPrefix is the first token of every API subject.
	SubjectPrefix = "api"

	// HeaderPath carries the request path and query (authoritative).
	HeaderPath = "X-Path"
	// HeaderStatus carries the HTTP status code on the first reply message.
	HeaderStatus = "X-Status"
	// HeaderSeq numbers the messages of a chunked body from zero.
	HeaderSeq = "X-Seq"
	// HeaderEnd marks the last message of a chunked body.
	HeaderEnd = "X-End"
	// HeaderBodyStream names the subject a service pulls request body
	// chunks from when the body does not fit in the request message.
	HeaderBodyStream = "X-Body-Stream"
	// HeaderTransport is set on every bridged request so handlers and logs
	// can tell a NATS call from an HTTPS call.
	HeaderTransport = "X-Transport"
	// HeaderAccountID is the account header the ID middleware reads.
	HeaderAccountID = "X-Blackbox-AccountID"

	// TransportNATS is the value of HeaderTransport.
	TransportNATS = "nats"

	// DefaultQueueGroup is the queue group instances of a service share.
	DefaultQueueGroup = "api"
	// DefaultTimeout bounds the wait for each message of a chunked body.
	DefaultTimeout = 30 * time.Second
	// DefaultChunkSize is the body chunk size unless the connection's max
	// payload forces a smaller one.
	DefaultChunkSize = 256 << 10
	// chunkHeadroom is reserved for headers inside the max payload.
	chunkHeadroom = 16 << 10
	// DefaultVersion is the micro service version when none is given.
	DefaultVersion = "1.0.0"
)

// Methods lists the HTTP methods served over NATS; each is a micro endpoint.
var Methods = []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD"}

var (
	// ErrNoService is returned when no instance of the service answered.
	ErrNoService = errors.New("natsapi: no service responded")
	// ErrBadURL is returned for a request URL that is not nats://{service}/path.
	ErrBadURL = errors.New("natsapi: request URL must be nats://{service}/api/...")
	// ErrNotConnected is returned when the transport has no connection.
	ErrNotConnected = errors.New("natsapi: not connected")
)

var serviceNameRegexp = regexp.MustCompile(`^[A-Za-z0-9\-_]+$`)

// ValidService reports whether name can be a service token and micro name.
func ValidService(name string) bool {
	return serviceNameRegexp.MatchString(name)
}

// ResourceToken returns the subject token for a request path: the first
// segment after the /api/{version}/ prefix, with characters that cannot
// appear in a subject token replaced by "_", or "_" when there is none.
// The query string is ignored.
func ResourceToken(path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	path = strings.TrimPrefix(path, "/api/")
	segments := strings.Split(path, "/")
	if len(segments) > 0 && isVersion(segments[0]) {
		segments = segments[1:]
	}
	for _, seg := range segments {
		if seg != "" {
			return sanitizeToken(seg)
		}
	}
	return "_"
}

func isVersion(seg string) bool {
	if len(seg) < 2 || seg[0] != 'v' {
		return false
	}
	for _, c := range seg[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func sanitizeToken(seg string) string {
	var b strings.Builder
	for _, c := range seg {
		switch c {
		case ' ', '.', '*', '>', '\t', '\r', '\n':
			b.WriteByte('_')
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// Subject returns the request subject for a service, method, and path.
func Subject(service, method, path string) (string, error) {
	if !ValidService(service) {
		return "", fmt.Errorf("natsapi: invalid service name %q", service)
	}
	if !strings.HasPrefix(path, "/api/") {
		return "", fmt.Errorf("natsapi: path %q is not under /api/", path)
	}
	method = strings.ToUpper(method)
	if method == "" {
		method = "GET"
	}
	return fmt.Sprintf("%s.%s.%s.%s", SubjectPrefix, service, method, ResourceToken(path)), nil
}

// EndpointSubject is the wildcard subject a service subscribes to for one
// method.
func EndpointSubject(service, method string) string {
	return fmt.Sprintf("%s.%s.%s.>", SubjectPrefix, service, strings.ToUpper(method))
}

// URL builds the request URL the Transport understands.
func URL(service, path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return "nats://" + service + path
}

// parseSubject splits api.{service}.{METHOD}.{resource} into its tokens.
func parseSubject(subject string) (service, method, resource string, ok bool) {
	tokens := strings.Split(subject, ".")
	if len(tokens) < 4 || tokens[0] != SubjectPrefix {
		return "", "", "", false
	}
	return tokens[1], tokens[2], strings.Join(tokens[3:], "."), true
}

// splitURL turns nats://{service}/path?query into its parts.
func splitURL(u *url.URL) (service, path string, err error) {
	if u == nil || u.Scheme != "nats" || u.Host == "" {
		return "", "", ErrBadURL
	}
	return u.Host, u.RequestURI(), nil
}

// protocolHeaders are the natsapi headers that are not HTTP headers and are
// stripped on both sides of the bridge.
var protocolHeaders = map[string]bool{
	HeaderPath:       true,
	HeaderStatus:     true,
	HeaderSeq:        true,
	HeaderEnd:        true,
	HeaderBodyStream: true,
}

// isProtocolHeader takes a canonical HTTP header key.
func isProtocolHeader(key string) bool {
	if protocolHeaders[key] {
		return true
	}
	// NATS and micro reserve their own namespace.
	return strings.HasPrefix(key, "Nats-")
}
