package admin

import (
	"encoding/base64"
	"testing"
	"testing/fstest"

	"github.com/bboxsys/platform/server"
)

func TestHostBrandingMetadata(t *testing.T) {
	logo := []byte("<svg/>")
	tokens := ":root {\n  --brand-hue: 155;\n}\n"
	withTokens := fstest.MapFS{BrandTokensFile: {Data: []byte(tokens)}, "index.html": {Data: []byte("<html>")}}
	withoutTokens := fstest.MapFS{"index.html": {Data: []byte("<html>")}}

	t.Run("reads the token block from the embedded SPA", func(t *testing.T) {
		md := HostBranding{Name: "Acme", LogoSVG: logo, UI: withTokens, Brand: "blue"}.Metadata()
		if md["branding_name"] != "Acme" {
			t.Errorf("name: %v", md["branding_name"])
		}
		want := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(logo)
		if md["branding_logo_url"] != want {
			t.Errorf("logo: %v", md["branding_logo_url"])
		}
		if md["branding_css"] != tokens {
			t.Errorf("css: %q", md["branding_css"])
		}
		if _, ok := md["branding_brand"]; ok {
			t.Errorf("a token block should replace the preset, got %v", md["branding_brand"])
		}
	})

	t.Run("falls back to the preset without a token block", func(t *testing.T) {
		md := HostBranding{Name: "Orange", LogoURL: "https://x/logo.svg", UI: withoutTokens, Brand: "orange"}.Metadata()
		if md["branding_brand"] != "orange" {
			t.Errorf("brand: %v", md["branding_brand"])
		}
		if md["branding_logo_url"] != "https://x/logo.svg" {
			t.Errorf("logo url should win over an svg: %v", md["branding_logo_url"])
		}
		if _, ok := md["branding_css"]; ok {
			t.Errorf("no css expected, got %q", md["branding_css"])
		}
	})

	t.Run("an explicit block wins and nil UI is fine", func(t *testing.T) {
		md := HostBranding{Name: "X", CSS: ":root { --brand-hue: 1; }", UI: withTokens}.Metadata()
		if md["branding_css"] != ":root { --brand-hue: 1; }" {
			t.Errorf("css: %q", md["branding_css"])
		}
		md = HostBranding{Name: "X"}.Metadata()
		if len(md) != 1 {
			t.Errorf("only the name expected, got %v", md)
		}
	})

	t.Run("NewAdminServer applies it", func(t *testing.T) {
		keyPEM, certPEM := selfSignedCert(t)
		md := map[string]interface{}{
			"storeLocation":        t.TempDir() + "/admin.db",
			"tokenValidMinutes":    60,
			"accountServerURL":     "https://localhost:0",
			"operatorServiceURL":   "https://localhost:0",
			"defaultAdminEmail":    testAdminEmail,
			"defaultAdminPassword": testAdminPassword,
		}
		for k, v := range (HostBranding{Name: "Acme", UI: withTokens}).Metadata() {
			md[k] = v
		}
		srv, err := NewAdminServer(&server.ServerOptions{
			Application: "unit_testing", Component: "admin_server",
			PrivateKey: keyPEM, Certificate: certPEM, CA: certPEM,
			HTTPPort: 0, Messenger: false, Metadata: md,
		})
		if err != nil {
			t.Fatal(err)
		}
		if srv.branding.Name != "Acme" || srv.branding.CSS != tokens {
			t.Errorf("branding: %+v", srv.branding)
		}
	})
}
