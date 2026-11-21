//go:build darwin

package pki

import (
	"fmt"

	"github.com/bboxsys/platform/utils"
)

func installPlatformCA(certFile string) error {
	cmd := utils.CommandWithSudo("security", "add-trusted-cert", "-d", "-k", "/Library/Keychains/System.keychain", certFile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("security add-trusted-cert failed: %w\n%s", err, out)
	}
	return nil
}

func uninstallPlatformCA(certFile string) error {
	cmd := utils.CommandWithSudo("security", "remove-trusted-cert", "-d", certFile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("security remove-trusted-cert failed: %w\n%s", err, out)
	}
	return nil
}
