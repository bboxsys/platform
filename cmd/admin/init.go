package main

import (
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bboxsys/platform/pki"
)

// initConfig seeds a development profile: a self-signed localhost certificate
// and a minimal TOML config next to it. Existing files are left alone so the
// command is safe to run on every start.
func initConfig(configFile string, port int) error {
	if configFile == "" {
		return fmt.Errorf("-init requires -config")
	}
	dir := filepath.Dir(configFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	certFile := filepath.Join(dir, "localhost.cert.pem")
	keyFile := filepath.Join(dir, "localhost.key.pem")
	if _, err := os.Stat(certFile); os.IsNotExist(err) {
		cert, key, err := pki.NewSelfSignedCertificatePair("localhost", "platform")
		if err != nil {
			return fmt.Errorf("generating certificate: %w", err)
		}
		if err := pki.WriteCertificateAndPrivateKeyToDisk(cert, key, "localhost", dir); err != nil {
			return fmt.Errorf("writing certificate: %w", err)
		}
		fmt.Printf("wrote %s\n", certFile)
	}

	// Agent registration signs a per-host certificate with an intermediate CA.
	// Seed a development root and intermediate so `agent register` works
	// against this profile.
	caCertFile := filepath.Join(dir, "ca.cert.pem")
	caKeyFile := filepath.Join(dir, "ca.key.pem")
	intCertFile := filepath.Join(dir, "intermediate.cert.pem")
	intKeyFile := filepath.Join(dir, "intermediate.key.pem")
	if _, err := os.Stat(intCertFile); os.IsNotExist(err) {
		if err := writeDevIntermediateCA(caCertFile, caKeyFile, intCertFile, intKeyFile); err != nil {
			return fmt.Errorf("generating intermediate CA: %w", err)
		}
		fmt.Printf("wrote %s\n", intCertFile)
	}

	// Everything in the profile verifies TLS peers against one bundle: the
	// localhost certificate (admin server and controller), plus the dev
	// root and intermediate that sign agent certificates at registration.
	// With it the controller accepts an agent's client certificate and the
	// agent, which receives this bundle too, trusts the controller.
	trustFile := filepath.Join(dir, "trust.pem")
	if err := writeTrustBundle(trustFile, certFile, intCertFile, caCertFile); err != nil {
		return fmt.Errorf("writing trust bundle: %w", err)
	}

	if _, err := os.Stat(configFile); err == nil {
		if err := ensureIntermediateConfig(configFile, caCertFile, intCertFile, intKeyFile); err != nil {
			return err
		}
		if err := ensureTrustBundleConfig(configFile, certFile, trustFile); err != nil {
			return err
		}
		if err := ensureControllerConfig(configFile, port); err != nil {
			return err
		}
		return ensureQueueConfig(configFile, port)
	}
	body := fmt.Sprintf(`[app]
name = "platform"
hostname = "localhost"
root_path = %q

[admin_server]
port = %d
url = "https://localhost:%d"
account_server = "https://localhost:%d"
admin_email = "admin@localhost"
admin_temp_password = "changeme"
allow_self_service_accounts = true
invitation_default_ttl_hours = 72
oauth_enabled = false
messenger = true
nats_api = true
intermediate_ca_file = %q
intermediate_cert_file = %q
intermediate_key_file = %q

[security]
cert_file = %q
key_file = %q
ca_file = %q

%s
%s
[logging]
max_entries = 100000
level = "debug"

[metrics]
max_entries = 100000
collect_interval_seconds = 30

# The AI assistant (docs/AI.md). Model and embedding credentials are API
# tokens in the vault, not config; these are the defaults.
[ai]
# enabled = true
# effort = "medium"   # unset: each credential's ai_effort tag, else medium
# allow_writes = false
# max_turns = 25
`, dir, port, port, port, caCertFile, intCertFile, intKeyFile, certFile, keyFile, trustFile, controllerSection(port), queueSection(port))
	if err := os.WriteFile(configFile, []byte(body), 0644); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", configFile)
	return nil
}

// writeDevIntermediateCA creates a self-signed development root CA and an
// intermediate signed by it, writing all four PEM files. The root key stays on
// disk only because this is a development profile.
func writeDevIntermediateCA(caCertFile, caKeyFile, intCertFile, intKeyFile string) error {
	ca, err := pki.NewCA("platform-dev-ca", "platform")
	if err != nil {
		return err
	}
	caKeyDER, err := x509.MarshalPKCS8PrivateKey(ca.PrivateKey)
	if err != nil {
		return err
	}
	if err := pki.WriteCertAndKeyToFiles(ca.Certificate.Raw, caKeyDER, caCertFile, caKeyFile); err != nil {
		return err
	}
	intCert, intKey, err := ca.NewIntermediateX509Certificate("platform-dev-intermediate", []string{"platform-dev-intermediate"}, nil)
	if err != nil {
		return err
	}
	intKeyDER, err := x509.MarshalPKCS8PrivateKey(intKey)
	if err != nil {
		return err
	}
	return pki.WriteCertAndKeyToFiles(intCert.Raw, intKeyDER, intCertFile, intKeyFile)
}

// writeTrustBundle concatenates the given PEM files into trustFile. It
// rewrites the bundle whenever its inputs changed so a regenerated
// certificate is picked up on the next start.
func writeTrustBundle(trustFile string, pemFiles ...string) error {
	var bundle []byte
	for _, f := range pemFiles {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		bundle = append(bundle, b...)
		if len(b) > 0 && b[len(b)-1] != '\n' {
			bundle = append(bundle, '\n')
		}
	}
	if existing, err := os.ReadFile(trustFile); err == nil && string(existing) == string(bundle) {
		return nil
	}
	if err := os.WriteFile(trustFile, bundle, 0644); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", trustFile)
	return nil
}

// ensureTrustBundleConfig points security.ca_file at the trust bundle in a
// profile an earlier init wrote with the bare localhost certificate.
func ensureTrustBundleConfig(configFile, certFile, trustFile string) error {
	raw, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}
	body := string(raw)
	old := fmt.Sprintf("ca_file = %q\n", certFile)
	if !strings.Contains(body, old) {
		return nil
	}
	body = strings.Replace(body, old, fmt.Sprintf("ca_file = %q\n", trustFile), 1)
	if err := os.WriteFile(configFile, []byte(body), 0644); err != nil {
		return err
	}
	fmt.Printf("pointed security.ca_file at %s in %s\n", trustFile, configFile)
	return nil
}

// ensureIntermediateConfig adds the intermediate CA keys to an existing
// profile written before -init seeded one. It inserts them right after the
// [admin_server] header and leaves everything else untouched.
func ensureIntermediateConfig(configFile, caCertFile, intCertFile, intKeyFile string) error {
	raw, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}
	body := string(raw)
	if strings.Contains(body, "intermediate_cert_file") {
		return nil
	}
	header := "[admin_server]\n"
	idx := strings.Index(body, header)
	if idx < 0 {
		return fmt.Errorf("%s: no [admin_server] section; add intermediate_{ca,cert,key}_file by hand", configFile)
	}
	insert := fmt.Sprintf("intermediate_ca_file = %q\nintermediate_cert_file = %q\nintermediate_key_file = %q\n", caCertFile, intCertFile, intKeyFile)
	body = body[:idx+len(header)] + insert + body[idx+len(header):]
	if err := os.WriteFile(configFile, []byte(body), 0644); err != nil {
		return err
	}
	fmt.Printf("added intermediate CA to %s\n", configFile)
	return nil
}

// controllerSection is the embedded NATS controller for a development
// profile: client port and monitoring port right after the admin port and
// the admin server as account resolver. The resolver connection is verified
// against security.ca_file, the profile's trust bundle, which holds the
// self-signed localhost certificate itself, so verification stays on.
func controllerSection(adminPort int) string {
	return fmt.Sprintf(`[controller]
embedded = true
port = %d
monitoring_port = %d
url = "nats://localhost:%d"
account_server = "https://localhost:%d/jwt/v1/accounts/"
insecure_skip_verify = false
`, adminPort+1, adminPort+2, adminPort+1, adminPort)
}

// insecureSeededBefore is the line an earlier init wrote into the controller
// section before the controller trusted security.ca_file for the resolver.
const insecureSeededBefore = "insecure_skip_verify = true\n"

// ensureControllerConfig adds the embedded controller to an existing profile
// written before -init seeded one, and turns the messenger on so logs and
// metrics flow through it. Profiles that already have a [controller] section
// are left alone.
func ensureControllerConfig(configFile string, adminPort int) error {
	raw, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}
	body := string(raw)
	if idx := strings.Index(body, "[controller]\nembedded = true\n"); idx >= 0 {
		// A profile this init wrote earlier. Turn resolver verification back
		// on now that the controller trusts the profile's own certificate.
		section := body[idx:]
		if strings.Contains(section, insecureSeededBefore) {
			body = body[:idx] + strings.Replace(section, insecureSeededBefore, "insecure_skip_verify = false\n", 1)
			if err := os.WriteFile(configFile, []byte(body), 0644); err != nil {
				return err
			}
			fmt.Printf("enabled resolver TLS verification in %s\n", configFile)
		}
		return nil
	}
	if strings.Contains(body, "[controller]") {
		return nil
	}
	body = strings.Replace(body, "\nmessenger = false\n", "\nmessenger = true\n", 1)
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	body += "\n" + controllerSection(adminPort)
	if err := os.WriteFile(configFile, []byte(body), 0644); err != nil {
		return err
	}
	fmt.Printf("added embedded controller to %s\n", configFile)
	return nil
}

// queueSection is the job queue relay service for a development profile, on
// the port after the controller's monitoring port. The admin publishes it as
// queue_service and the Queue page reads it through the admin's proxy.
func queueSection(adminPort int) string {
	return fmt.Sprintf(`[queue_server]
port = %d
nats_api = true
`, adminPort+3)
}

// ensureQueueConfig adds the queue service to an existing profile written
// before -init seeded one. Profiles that already have a [queue_server]
// section are left alone.
func ensureQueueConfig(configFile string, adminPort int) error {
	raw, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}
	body := string(raw)
	if strings.Contains(body, "[queue_server]") {
		return nil
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	body += "\n" + queueSection(adminPort)
	if err := os.WriteFile(configFile, []byte(body), 0644); err != nil {
		return err
	}
	fmt.Printf("added queue server to %s\n", configFile)
	return nil
}
