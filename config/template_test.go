package config

import (
	"testing"
)

func TestDefaultUserConfig(t *testing.T) {
	configTemplate := DefaultConfig("localhost", "user", 8900)
	err := configTemplate.WriteToDisk("./default-user-sample.json")
	if err != nil {
		t.Errorf("error writing default user config template to disk: %v", err)
	}

}
func TestDefaultServiceConfig(t *testing.T) {
	configTemplate := DefaultConfig("localhost", "service", 8900)
	err := configTemplate.WriteToDisk("./default-service-sample.json")
	if err != nil {
		t.Errorf("error writing default service config template to disk: %v", err)
	}

}
