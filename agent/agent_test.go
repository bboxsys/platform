package agent

import (
	"bytes"
	"strings"
	"testing"
)

func TestOptionsDefaults(t *testing.T) {
	o := Options{}.withDefaults()
	if o.Name != DefaultName {
		t.Fatalf("Name = %q, want %q", o.Name, DefaultName)
	}
	if !strings.Contains(o.Short, DefaultName) || !strings.Contains(o.Long, DefaultName) {
		t.Fatalf("help text should mention the name: short=%q long=%q", o.Short, o.Long)
	}

	o = Options{Name: "acme-agent", Short: "s", Long: "l"}.withDefaults()
	if o.Name != "acme-agent" || o.Short != "s" || o.Long != "l" {
		t.Fatalf("explicit options overwritten: %+v", o)
	}
}

func TestNewCommandWiring(t *testing.T) {
	root := NewCommand(Options{Name: "test-agent"})
	if root.Use != "test-agent" {
		t.Fatalf("Use = %q", root.Use)
	}
	for _, name := range []string{"register", "run"} {
		sub, _, err := root.Find([]string{name})
		if err != nil || sub == nil || sub.Name() != name {
			t.Fatalf("subcommand %q not found: %v", name, err)
		}
	}
	for _, flag := range []string{"config", "as-service"} {
		if root.PersistentFlags().Lookup(flag) == nil {
			t.Fatalf("persistent flag --%s missing", flag)
		}
	}
	reg, _, _ := root.Find([]string{"register"})
	for _, flag := range []string{"account-id", "admin-server-url", "token"} {
		if reg.Flags().Lookup(flag) == nil {
			t.Fatalf("register flag --%s missing", flag)
		}
	}

	// Two commands must not share flag state.
	other := NewCommand(Options{Name: "other-agent"})
	if other.PersistentFlags().Lookup("config") == root.PersistentFlags().Lookup("config") {
		t.Fatal("commands share flag instances")
	}

	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "test-agent") {
		t.Fatalf("help output missing name: %s", out.String())
	}
}
