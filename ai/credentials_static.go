package ai

import (
	"context"
	"fmt"
)

// StaticCredentials is a CredentialResolver for a host without the admin
// vault: a host agent or a product service that reads an API key from its
// own configuration. Every caller gets the same credentials. A nil
// ModelCredential or EmbeddingCredential answers ErrNoCredential, which
// turns the assistant (or retrieval) off rather than failing.
//
// The fields carry a Credential suffix because a Go type cannot have a
// field and a method of the same name, and the methods are the
// CredentialResolver interface.
//
// A name passed to Model or Embedding must match the credential's Name
// when both are set; an empty name always selects the configured one.
type StaticCredentials struct {
	ModelCredential      *Credential
	EmbeddingCredential  *Credential
	MCPServerCredentials []Credential
}

var (
	_ CredentialResolver = StaticCredentials{}
	_ CredentialLister   = StaticCredentials{}
)

// Model returns the configured model credential.
func (s StaticCredentials) Model(_ context.Context, _, _, name string) (Credential, error) {
	return staticCredential(s.ModelCredential, ResourceTypeModel, name)
}

// Models returns the configured model credential as a list of one, or an
// empty list.
func (s StaticCredentials) Models(context.Context, string, string) ([]Credential, error) {
	c, err := staticCredential(s.ModelCredential, ResourceTypeModel, "")
	if err != nil {
		return []Credential{}, nil
	}
	return []Credential{c}, nil
}

// Embedding returns the configured embedding credential.
func (s StaticCredentials) Embedding(_ context.Context, _, _, name string) (Credential, error) {
	return staticCredential(s.EmbeddingCredential, ResourceTypeEmbedding, name)
}

// MCPServers returns a copy of the configured MCP server credentials.
func (s StaticCredentials) MCPServers(context.Context, string, string) ([]Credential, error) {
	out := make([]Credential, len(s.MCPServerCredentials))
	for i, c := range s.MCPServerCredentials {
		if c.ResourceType == "" {
			c.ResourceType = ResourceTypeMCPServer
		}
		out[i] = c
	}
	return out, nil
}

func staticCredential(c *Credential, resourceType, name string) (Credential, error) {
	if c == nil {
		return Credential{}, ErrNoCredential
	}
	if name != "" && c.Name != "" && c.Name != name {
		return Credential{}, fmt.Errorf("%w: %s %q", ErrNoCredential, resourceType, name)
	}
	out := *c
	if out.ResourceType == "" {
		out.ResourceType = resourceType
	}
	return out, nil
}
