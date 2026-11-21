package utils

import (
	"os"
	"os/exec"
	"os/user"
)

func CommandWithSudo(cmd ...string) *exec.Cmd {
	if u, err := user.Current(); err == nil && u.Uid == "0" {
		return exec.Command(cmd[0], cmd[1:]...)
	}
	if !BinaryExists("sudo") {
		return exec.Command(cmd[0], cmd[1:]...)
	}
	return exec.Command("sudo", append([]string{"--prompt=Sudo password:", "--"}, cmd...)...)
}

func BinaryExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func PathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
