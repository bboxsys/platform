package admin

import (
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"

	"github.com/bboxsys/platform/server"
)

// ServeUI makes the admin server host a single-page application from fsys
// on the same origin as the API. Call before Start. Requests that match no
// API route and no static file fall back to index.html so client-side
// routing works.
func (adminServer *AdminServer) ServeUI(fsys fs.FS) {
	adminServer.uiFS = fsys
}

// Branding lets a host application present the shared admin console under its
// own name and mark. It is served unauthenticated so the SPA can render the
// correct brand before a session exists.
type Branding struct {
	Name    string `json:"name"`
	LogoURL string `json:"logoUrl,omitempty"`
	// Brand names a @bboxsys/ui preset ("blue", the default, or "orange") the
	// console applies on top of its stylesheet, so the host's accent carries
	// into the console rather than the kit default.
	Brand string `json:"brand,omitempty"`
	// CSS is a stylesheet the console injects after its own, for a host whose
	// brand is not a kit preset: the same :root / .dark token block the host's
	// SPA imports, nothing else (no fonts by relative URL; they would resolve
	// against the console's origin).
	CSS string `json:"css,omitempty"`
}

// BrandTokensFile is the stylesheet @bboxsys/ui's brandTokens() Vite plugin
// writes into a product SPA's build output: the :root and .dark brand token
// blocks of the SPA's src/brand.css, and nothing else. HostBranding reads it
// from the embedded SPA so the console paints in the product's brand.
const BrandTokensFile = "brand-tokens.css"

// HostBranding is what a product that embeds the admin console gives it:
// the name and mark for the sidebar, and the brand as either a kit preset or
// the product's own token block. Metadata turns it into the branding_* keys
// NewAdminServer reads, so a host writes
//
//	md := admin.HostBranding{Name: "Acme", LogoSVG: logo, UI: acmevue.UIFS()}.Metadata()
//
// and merges md into its ServerOptions.Metadata.
type HostBranding struct {
	// Name is the product name shown as "{Name} Admin". Required.
	Name string
	// LogoURL is the mark's URL. Prefer LogoSVG: an inline data URI works on
	// every origin the console is served from.
	LogoURL string
	// LogoSVG is the product's mark, inlined as a data URI when LogoURL is
	// empty.
	LogoSVG []byte
	// Brand names a @bboxsys/ui preset ("blue", "orange") for a product that
	// uses one. Ignored when a token block is found.
	Brand string
	// UI is the product's built SPA (the fs.FS it serves); BrandTokensFile
	// is read from its root when present. A SPA built without the
	// brandTokens() plugin has none, and the preset or default applies.
	UI fs.FS
	// CSS is an explicit token block, taking precedence over UI.
	CSS string
}

// Metadata returns the branding_* metadata entries for ServerOptions. Only
// set entries are returned, so merging it never clears a host's own value.
func (b HostBranding) Metadata() map[string]interface{} {
	md := map[string]interface{}{}
	if b.Name != "" {
		md["branding_name"] = b.Name
	}
	switch {
	case b.LogoURL != "":
		md["branding_logo_url"] = b.LogoURL
	case len(b.LogoSVG) > 0:
		md["branding_logo_url"] = "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(b.LogoSVG)
	}
	css := b.CSS
	if css == "" && b.UI != nil {
		if data, err := fs.ReadFile(b.UI, BrandTokensFile); err == nil {
			css = string(data)
		}
	}
	if strings.TrimSpace(css) != "" {
		md["branding_css"] = css
	} else if b.Brand != "" {
		md["branding_brand"] = b.Brand
	}
	return md
}

func (adminServer *AdminServer) addUIHandlers() {
	adminServer.Router.HandleFunc("/serviceendpoints/", adminServer.serviceEndpointsBootstrap).Methods("GET")
	adminServer.Router.HandleFunc("/branding/", adminServer.brandingBootstrap).Methods("GET")
	if adminServer.uiFS == nil {
		adminServer.Router.HandleFunc("/", server.EndpointNotImplemented).Methods("GET")
		return
	}
	// Serve the SPA (static assets + index.html fallback) ONLY as the
	// NotFoundHandler. A greedy PathPrefix("/") GET route would be matched
	// before API GET routes registered later (gorilla/mux matches in
	// registration order, and addUIHandlers runs mid-AddHandlers), so it would
	// return index.html for API calls like GET /api/v1/login. SPAHandler already
	// serves assets and falls back to index.html for unknown paths, so the
	// NotFoundHandler alone is sufficient and never shadows an API route.
	adminServer.Router.NotFoundHandler = server.SPAHandler(adminServer.uiFS)
}

// serviceEndpointsBootstrap is unauthenticated: the SPA needs the endpoint
// map before it has a session. Only kinds and URLs are exposed.
func (adminServer *AdminServer) serviceEndpointsBootstrap(w http.ResponseWriter, r *http.Request) {
	endpoints, err := adminServer.st.GetServiceEndpoints()
	if err != nil {
		endpoints = map[string]ServiceEndpoint{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(endpoints)
}

// brandingBootstrap is unauthenticated: the SPA needs the brand name and mark
// before it has a session so the login screen renders correctly.
func (adminServer *AdminServer) brandingBootstrap(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(adminServer.branding)
}
