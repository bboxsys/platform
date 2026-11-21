package docsite

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/platform/user"
)

const (
	// SessionPath is the route under the prefix that establishes and ends
	// the documentation session: POST with the user's bearer sets the
	// cookie, DELETE clears it, GET reports whether one is set.
	SessionPath = "_session"
	// DefaultCookieName is the session cookie's name.
	DefaultCookieName = "docs_session"
	// DefaultMaxAge bounds a session, however long the bearer lasts.
	DefaultMaxAge = 24 * time.Hour
	// DefaultMaxSessions bounds the session table.
	DefaultMaxSessions = 10000
	// AccountHeader is the header the SPA sends the account id in.
	AccountHeader = "X-Blackbox-AccountID"
)

// TrustedAccounts reports whether a token issuer (an account public key)
// is one this host accepts tokens from. *admin/client.Auth satisfies it.
type TrustedAccounts interface {
	IsPublicKeyTrusted(publicKey string) bool
}

// UserLookup finds a user by account and public key, for Identity.Roles.
// *admin.Store and any admin/client.IdentityStore satisfy it.
type UserLookup interface {
	GetUserByPublicKey(accountID, publicKey string) (*user.User, error)
}

// HandlerOptions configure Handler.
type HandlerOptions struct {
	// Prefix is the path the site is mounted at, without a trailing slash
	// ("/docs"). Required; the Starlight build must use the same base.
	Prefix string
	// Auth decides which accounts' tokens are accepted when a session is
	// established. Required unless Validate is set.
	Auth TrustedAccounts
	// Users fills Identity.Roles for Authorize. A NATS user JWT carries no
	// roles, so without a lookup Roles is empty and Authorize can only
	// decide by account and user. Optional.
	Users UserLookup
	// Validate replaces the default bearer validation (signature, trusted
	// issuer, lifetime, and the account header when present). Optional.
	Validate func(r *http.Request, token string) (*Identity, error)
	// Authorize decides whether an authenticated identity may read the
	// site; nil admits every user of a trusted account. It runs when the
	// session is established and on every page request.
	Authorize func(r *http.Request, id *Identity) bool
	// SignIn returns where a browser without a session is sent for a page
	// request, given the path (and query) it asked for: normally the SPA's
	// documentation gate, "/session/docs?next=...". Nil answers 401.
	SignIn func(next string) string
	// CookieName defaults to DefaultCookieName.
	CookieName string
	// MaxAge bounds a session's life; zero means DefaultMaxAge.
	MaxAge time.Duration
	// MaxSessions bounds the session table; zero means DefaultMaxSessions.
	MaxSessions int
	// Logger receives operational lines; nil logs as docsite/{project}.
	Logger *logger.Logger
}

type handler struct {
	site     *Site
	fsys     fs.FS
	opts     HandlerOptions
	sessions *sessions
	log      *logger.Logger
}

// Handler serves site under o.Prefix to browsers that hold a session.
//
// A session is established by the SPA: POST {prefix}/_session with the
// user's bearer (and the account header the kit's request helper sends)
// answers 204 and sets an HttpOnly, SameSite=Lax cookie scoped to the
// prefix; DELETE clears it. Page requests without a session are redirected
// to SignIn(next); asset requests answer 401. Pages are served as a built
// static site: directory indexes, a redirect onto the trailing slash,
// the site's 404.html for a missing page, immutable caching under
// _astro/, and precompressed variants when the build produced them.
func Handler(site *Site, o HandlerOptions) (http.Handler, error) {
	if site == nil {
		return nil, errors.New("docsite: site is nil")
	}
	o.Prefix = "/" + strings.Trim(o.Prefix, "/")
	if o.Prefix == "/" {
		return nil, errors.New("docsite: Prefix is required")
	}
	if o.Auth == nil && o.Validate == nil {
		return nil, errors.New("docsite: Auth (or Validate) is required")
	}
	if o.CookieName == "" {
		o.CookieName = DefaultCookieName
	}
	if o.MaxAge <= 0 {
		o.MaxAge = DefaultMaxAge
	}
	if o.Logger == nil {
		o.Logger = logger.NewLogger("docsite", site.Project)
	}
	h := &handler{site: site, fsys: site.FS(), opts: o, sessions: newSessions(o.MaxSessions, nil), log: o.Logger}
	if h.opts.Validate == nil {
		h.opts.Validate = h.validate
	}
	return h, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := path.Clean(r.URL.Path)
	if p == h.opts.Prefix {
		if !strings.HasSuffix(r.URL.Path, "/") {
			http.Redirect(w, r, h.opts.Prefix+"/", http.StatusMovedPermanently)
			return
		}
		p += "/"
	}
	if !strings.HasPrefix(p, h.opts.Prefix+"/") {
		http.NotFound(w, r)
		return
	}
	rel := strings.TrimPrefix(p, h.opts.Prefix+"/")
	if rel == SessionPath {
		h.serveSession(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := h.session(r)
	if id == nil || (h.opts.Authorize != nil && !h.opts.Authorize(r, id)) {
		h.denied(w, r, rel, id != nil)
		return
	}
	h.serveFile(w, r, rel)
}

// session returns the request's live identity, or nil.
func (h *handler) session(r *http.Request) *Identity {
	c, err := r.Cookie(h.opts.CookieName)
	if err != nil {
		return nil
	}
	return h.sessions.get(c.Value)
}

// denied answers a request without a usable session: a page request is
// sent to the sign-in gate, anything else gets a bare status.
func (h *handler) denied(w http.ResponseWriter, r *http.Request, rel string, forbidden bool) {
	status := http.StatusUnauthorized
	if forbidden {
		status = http.StatusForbidden
	}
	if forbidden || h.opts.SignIn == nil || isAsset(rel) {
		http.Error(w, http.StatusText(status), status)
		return
	}
	next := r.URL.RequestURI()
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, h.opts.SignIn(next), http.StatusFound)
}

// isAsset reports a request for a file rather than a page, which must not
// be redirected to an HTML sign-in: a script tag receiving a redirect to
// the SPA would try to run it.
func isAsset(rel string) bool {
	if strings.HasPrefix(rel, "_astro/") || strings.HasPrefix(rel, "pagefind/") {
		return true
	}
	switch path.Ext(rel) {
	case "", ".html":
		return false
	}
	return true
}

// serveSession handles {prefix}/_session.
func (h *handler) serveSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodPost:
		token := bearer(r)
		if token == "" {
			http.Error(w, "bearer token required", http.StatusUnauthorized)
			return
		}
		id, err := h.opts.Validate(r, token)
		if err != nil {
			h.log.InfoE().Err(err).Msg("docsite: session refused")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if h.opts.Authorize != nil && !h.opts.Authorize(r, id) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		now := time.Now()
		if id.Expires.IsZero() || id.Expires.After(now.Add(h.opts.MaxAge)) {
			id.Expires = now.Add(h.opts.MaxAge)
		}
		key, err := h.sessions.add(id)
		if err != nil {
			http.Error(w, "session unavailable", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     h.opts.CookieName,
			Value:    key,
			Path:     h.opts.Prefix,
			MaxAge:   int(time.Until(id.Expires).Seconds()),
			HttpOnly: true,
			Secure:   secure(r),
			SameSite: http.SameSiteLaxMode,
		})
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if c, err := r.Cookie(h.opts.CookieName); err == nil {
			h.sessions.remove(c.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name: h.opts.CookieName, Value: "", Path: h.opts.Prefix, MaxAge: -1,
			HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteLaxMode,
		})
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet, http.MethodHead:
		if id := h.session(r); id != nil && (h.opts.Authorize == nil || h.opts.Authorize(r, id)) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// validate is the default bearer check: the token's signature and
// lifetime (user.ValidateUserToken), an issuer among the trusted
// accounts, and the account header, when sent, matching that issuer.
func (h *handler) validate(r *http.Request, token string) (*Identity, error) {
	claims, err := user.DecodeUserClaims(token)
	if err != nil {
		return nil, err
	}
	issuer := claims.Issuer
	if account := r.Header.Get(AccountHeader); account != "" && account != issuer {
		return nil, fmt.Errorf("account header %q does not match the token issuer", account)
	}
	if !h.opts.Auth.IsPublicKeyTrusted(issuer) {
		return nil, fmt.Errorf("token issuer %q is not a trusted account", issuer)
	}
	claims, err = user.ValidateUserToken(token, issuer)
	if err != nil {
		return nil, err
	}
	id := &Identity{AccountID: issuer, UserID: claims.Subject}
	if claims.Expires > 0 {
		id.Expires = time.Unix(claims.Expires, 0)
	}
	if h.opts.Users != nil {
		u, err := h.opts.Users.GetUserByPublicKey(issuer, claims.Subject)
		if err != nil {
			return nil, fmt.Errorf("user %s: %w", claims.Subject, err)
		}
		id.Roles = append([]string(nil), u.Roles...)
	}
	return id, nil
}

func bearer(r *http.Request) string {
	field := strings.Fields(r.Header.Get("Authorization"))
	if len(field) == 2 && strings.EqualFold(field[0], "Bearer") {
		return field[1]
	}
	return ""
}

// secure reports whether the request arrived over TLS, directly or through
// a proxy that says so, which decides the cookie's Secure flag.
func secure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// serveFile serves rel from the built site.
func (h *handler) serveFile(w http.ResponseWriter, r *http.Request, rel string) {
	name := strings.Trim(path.Clean("/"+rel), "/")
	if name == "" {
		name = "index.html"
	}
	if strings.HasSuffix(name, ".gz") || strings.HasSuffix(name, ".br") {
		h.notFound(w, r)
		return
	}
	info, err := fs.Stat(h.fsys, name)
	switch {
	case err == nil && !info.IsDir():
		// A file.
	case err == nil && info.IsDir(), path.Ext(name) == "":
		// A directory index, which the browser must address with a
		// trailing slash so the page's relative links resolve.
		if _, err := fs.Stat(h.fsys, name+"/index.html"); err == nil {
			if !strings.HasSuffix(r.URL.Path, "/") {
				u := *r.URL
				u.Path += "/"
				http.Redirect(w, r, u.String(), http.StatusMovedPermanently)
				return
			}
			name += "/index.html"
			break
		}
		if _, err := fs.Stat(h.fsys, name+".html"); err == nil {
			name += ".html"
			break
		}
		h.notFound(w, r)
		return
	default:
		h.notFound(w, r)
		return
	}
	immutable := strings.HasPrefix(name, "_astro/")
	h.write(w, r, name, http.StatusOK, immutable)
}

// notFound serves the site's own 404 page when it has one.
func (h *handler) notFound(w http.ResponseWriter, r *http.Request) {
	if _, err := fs.Stat(h.fsys, "404.html"); err == nil {
		h.write(w, r, "404.html", http.StatusNotFound, false)
		return
	}
	http.NotFound(w, r)
}

// write sends one file, precompressed when the build produced a variant
// the client accepts.
func (h *handler) write(w http.ResponseWriter, r *http.Request, name string, status int, immutable bool) {
	ctype := mime.TypeByExtension(path.Ext(name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if immutable {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		// Pages are private to the session.
		w.Header().Set("Cache-Control", "private, no-cache")
	}
	w.Header().Set("Vary", "Accept-Encoding, Cookie")
	accept := r.Header.Get("Accept-Encoding")
	for _, enc := range []struct{ coding, ext string }{{"br", ".br"}, {"gzip", ".gz"}} {
		if !strings.Contains(accept, enc.coding) {
			continue
		}
		if f, err := h.fsys.Open(name + enc.ext); err == nil {
			defer f.Close()
			w.Header().Set("Content-Encoding", enc.coding)
			w.WriteHeader(status)
			if r.Method != http.MethodHead {
				io.Copy(w, f)
			}
			return
		}
	}
	f, err := h.fsys.Open(name)
	if err != nil {
		http.Error(w, "error opening file", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		io.Copy(w, f)
	}
}
