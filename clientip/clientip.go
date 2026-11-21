package clientip

import (
	"context"
	"net"
	"net/http"
	"strings"
)

// trustForwardedFor controls whether the X-Forwarded-For header is honored.
// It must only be enabled when the service sits behind a trusted proxy that
// sets the header (for example a Cloudflare tunnel); otherwise any client can
// spoof the header and forge the IP recorded in audit and lockout records.
// It defaults to true to match the standard reverse-proxy deployment; set it
// to false with SetTrustForwardedFor for direct-to-origin deployments.
var trustForwardedFor = true

// SetTrustForwardedFor configures whether X-Forwarded-For is trusted. Call
// this at startup based on deployment topology.
func SetTrustForwardedFor(trust bool) {
	trustForwardedFor = trust
}

type ClientIP struct {
}

func (clientip *ClientIP) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(NewContext(r.Context(), FromRequest(r)))
		next.ServeHTTP(w, r)
	})
}

func NewClientIP(component string) *ClientIP {
	clientip := ClientIP{}
	return &clientip
}

// FromRequest returns the client IP for r. When X-Forwarded-For is trusted and
// present it uses the left-most (original client) entry of the header; a
// multi-hop value such as "client, proxy1, proxy2" is parsed correctly rather
// than failing to parse. Otherwise it falls back to the connection's RemoteAddr.
func FromRequest(r *http.Request) net.IP {
	if trustForwardedFor {
		if xff := r.Header.Get("x-forwarded-for"); xff != "" {
			// The left-most entry is the originating client; the rest are
			// proxies. Trim each candidate and return the first that parses.
			for _, part := range strings.Split(xff, ",") {
				if ip := net.ParseIP(strings.TrimSpace(part)); ip != nil {
					return ip
				}
			}
		}
	}
	if r.RemoteAddr != "" {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			// RemoteAddr may already be a bare IP with no port.
			if parsed := net.ParseIP(r.RemoteAddr); parsed != nil {
				return parsed
			}
			return net.ParseIP("0.0.0.0")
		}
		return net.ParseIP(ip)
	}
	return net.ParseIP("0.0.0.0")
}

// FromRequestIPv4 extracts the IPv4 address from the request as a string.
// Returns the IPv4 string if the client IP is IPv4 or IPv4-mapped IPv6,
// otherwise returns an empty string.
func FromRequestIPv4(r *http.Request) string {
	ip := FromRequest(r)
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ""
}

// The key type is unexported to prevent collisions with context keys defined in
// other packages.
type key int

// clientIPkey is the context key for the client IP address.  Its value of zero is
// arbitrary.  If this package defined other context keys, they would have
// different integer values.
const clientIPKey key = 0

// NewContext returns a new Context carrying userIP.
func NewContext(ctx context.Context, clientIP net.IP) context.Context {
	return context.WithValue(ctx, clientIPKey, clientIP)
}

// FromContext extracts the user IP address from ctx, if present.
func FromContext(ctx context.Context) net.IP {
	// ctx.Value returns nil if ctx has no value for the key;
	// the net.IP type assertion returns ok=false for nil.
	clientIP, _ := ctx.Value(clientIPKey).(net.IP)
	return clientIP
}
