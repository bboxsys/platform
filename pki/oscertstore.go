package pki

// OSCertStore is retained for backward compatibility.
// Trust store operations are now handled by InstallCA/UninstallCA
// in truststore.go with platform-specific implementations.
type OSCertStore struct {
	CAROOT             string
	IgnoreCheckFailure bool
}
