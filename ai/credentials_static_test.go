package ai

import (
	"context"
	"errors"
	"testing"
)

func TestStaticCredentials(t *testing.T) {
	ctx := context.Background()
	var empty StaticCredentials
	if _, err := empty.Model(ctx, "a", "u", ""); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("empty model: %v", err)
	}
	if _, err := empty.Embedding(ctx, "a", "u", ""); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("empty embedding: %v", err)
	}
	if got, err := empty.MCPServers(ctx, "a", "u"); err != nil || len(got) != 0 {
		t.Fatalf("empty mcp: %v %v", got, err)
	}

	s := StaticCredentials{
		ModelCredential:      &Credential{Name: "claude", Provider: ProviderAnthropic, Model: "claude-opus-5-5", Bearer: "k"},
		EmbeddingCredential:  &Credential{Name: "voyage", Provider: ProviderVoyage, Bearer: "v"},
		MCPServerCredentials: []Credential{{Name: "docs", Endpoint: "http://x"}},
	}
	m, err := s.Model(ctx, "a", "u", "")
	if err != nil || m.Name != "claude" || m.ResourceType != ResourceTypeModel || m.Bearer != "k" {
		t.Fatalf("model: %+v %v", m, err)
	}
	if m, err = s.Model(ctx, "a", "u", "claude"); err != nil || m.Model != "claude-opus-5-5" {
		t.Fatalf("model by name: %+v %v", m, err)
	}
	if _, err = s.Model(ctx, "a", "u", "other"); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("model by wrong name: %v", err)
	}
	e, err := s.Embedding(ctx, "a", "u", "")
	if err != nil || e.ResourceType != ResourceTypeEmbedding {
		t.Fatalf("embedding: %+v %v", e, err)
	}
	mcps, err := s.MCPServers(ctx, "a", "u")
	if err != nil || len(mcps) != 1 || mcps[0].ResourceType != ResourceTypeMCPServer {
		t.Fatalf("mcp: %+v %v", mcps, err)
	}
	mcps[0].Name = "changed"
	if s.MCPServerCredentials[0].Name != "docs" {
		t.Fatal("MCPServers must return a copy")
	}
	m.Bearer = "changed"
	if s.ModelCredential.Bearer != "k" {
		t.Fatal("Model must return a copy")
	}
}

func TestStaticCredentialsModels(t *testing.T) {
	ctx := context.Background()
	none, err := StaticCredentials{}.Models(ctx, "a", "u")
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("no credential: %v %v", none, err)
	}
	one, err := StaticCredentials{ModelCredential: &Credential{Name: "k", Model: "m"}}.Models(ctx, "a", "u")
	if err != nil || len(one) != 1 || one[0].Name != "k" || one[0].ResourceType != ResourceTypeModel {
		t.Errorf("one credential: %+v %v", one, err)
	}
}
