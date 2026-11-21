//go:build linux

package pki

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bboxsys/platform/utils"
)

type linuxTrustStore struct {
	certDir       string
	updateCommand string
	updateArgs    []string
}

func detectTrustStore() (*linuxTrustStore, error) {
	// RHEL/Fedora/CentOS
	if utils.PathExists("/etc/pki/ca-trust/source/anchors") {
		return &linuxTrustStore{
			certDir:       "/etc/pki/ca-trust/source/anchors",
			updateCommand: "update-ca-trust",
			updateArgs:    []string{"extract"},
		}, nil
	}

	// Debian/Ubuntu
	if utils.PathExists("/usr/local/share/ca-certificates") {
		return &linuxTrustStore{
			certDir:       "/usr/local/share/ca-certificates",
			updateCommand: "update-ca-certificates",
		}, nil
	}

	// Arch Linux
	if utils.PathExists("/etc/ca-certificates/trust-source/anchors") {
		return &linuxTrustStore{
			certDir:       "/etc/ca-certificates/trust-source/anchors",
			updateCommand: "trust",
			updateArgs:    []string{"extract-compat"},
		}, nil
	}

	return nil, fmt.Errorf("unable to detect trust store location; supported: RHEL/Fedora, Debian/Ubuntu, Arch")
}

func installPlatformCA(certFile string) error {
	ts, err := detectTrustStore()
	if err != nil {
		return err
	}

	destName := filepath.Base(certFile)
	// Debian requires .crt extension
	if ts.updateCommand == "update-ca-certificates" {
		ext := filepath.Ext(destName)
		if ext != ".crt" {
			destName = destName[:len(destName)-len(ext)] + ".crt"
		}
	}
	destPath := filepath.Join(ts.certDir, destName)

	// Copy the certificate to the trust store directory
	data, err := os.ReadFile(certFile)
	if err != nil {
		return fmt.Errorf("failed to read certificate: %w", err)
	}

	cmd := utils.CommandWithSudo("tee", destPath)
	cmd.Stdin = bytes.NewReader(data)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to copy certificate to %s: %w\n%s", destPath, err, out)
	}

	// Run the update command
	args := append([]string{ts.updateCommand}, ts.updateArgs...)
	updateCmd := utils.CommandWithSudo(args...)
	out, err := updateCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s failed: %w\n%s", ts.updateCommand, err, out)
	}

	return nil
}

func uninstallPlatformCA(certFile string) error {
	ts, err := detectTrustStore()
	if err != nil {
		return err
	}

	destName := filepath.Base(certFile)
	if ts.updateCommand == "update-ca-certificates" {
		ext := filepath.Ext(destName)
		if ext != ".crt" {
			destName = destName[:len(destName)-len(ext)] + ".crt"
		}
	}
	destPath := filepath.Join(ts.certDir, destName)

	// Remove the certificate
	rmCmd := utils.CommandWithSudo("rm", "-f", destPath)
	if out, err := rmCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to remove certificate from %s: %w\n%s", destPath, err, out)
	}

	// Run the update command
	args := append([]string{ts.updateCommand}, ts.updateArgs...)
	updateCmd := utils.CommandWithSudo(args...)
	out, err := updateCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s failed: %w\n%s", ts.updateCommand, err, out)
	}

	return nil
}
