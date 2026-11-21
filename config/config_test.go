package config

import (
	"fmt"
	"testing"
)

var (
	testDirectory = ".gounittests"
)

func TestGetHomeDir(t *testing.T) {
	homedir := GetHomedir()
	if homedir == "" {
		t.Errorf("not able to get homedir")
	} else {
		fmt.Printf("got homedir: %v\n", homedir)
	}
}

func TestFullConfigDir(t *testing.T) {
	c := New("user")
	dir := c.DefaultConfigDir()
	if dir == "" {
		t.Errorf("error creating config directory")
	} else {
		fmt.Printf("home directory exists or was created: %v\n", dir)
	}
}

func TestDefaultAgentConfigIsJSON(t *testing.T) {
	// The agent config is the admin server's JSON registration response
	// written verbatim, so the default filename must carry a .json extension
	// or viper tries to parse it as TOML.
	if DefaultAgentConfigFilename != "blackbox-agent.json" {
		t.Fatalf("DefaultAgentConfigFilename = %q, want blackbox-agent.json", DefaultAgentConfigFilename)
	}
	c := New("service")
	if got, want := c.DefaultAgentConfigFullPath(), DefaultServiceConfigDir+"/blackbox-agent.json"; got != want {
		t.Fatalf("service path = %q, want %q", got, want)
	}
}
