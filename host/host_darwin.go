package host

import (
	"runtime"
	"strings"
	"syscall"
)

const (
	hostIdPath = "/etc/machine-id"
)

func GetHost() (*Host, error) {
	var host Host
	host.OS = runtime.GOOS
	host.CPUs = runtime.NumCPU()
	hostname, err := GetHostname()
	if err != nil {
		return &host, err
	}
	host.Hostname = hostname
	return &host, nil
}

func (host *Host) updateUname() error {
	return nil
}

func (host *Host) updateLsbRelease() error {
	return nil
}

// GetHostID returns the platform UUID (kern.uuid), which is stable across
// reboots. /etc/machine-id is tried first so a host that has been given one
// keeps the same ID it registered with.
func GetHostID() (string, error) {
	if id, err := ReadFileString(hostIdPath); err == nil && strings.TrimSpace(id) != "" {
		return id, nil
	}
	id, err := syscall.Sysctl("kern.uuid")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(id), nil
}

func GetKernelCmdline() (string, error) {
	return "", nil
}
