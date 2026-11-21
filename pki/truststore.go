package pki

// InstallCA installs the CA certificate at the given path into the OS trust store.
// This requires elevated privileges on most platforms.
func InstallCA(certFile string) error {
	return installPlatformCA(certFile)
}

// UninstallCA removes a previously installed CA certificate from the OS trust store.
// This requires elevated privileges on most platforms.
func UninstallCA(certFile string) error {
	return uninstallPlatformCA(certFile)
}
