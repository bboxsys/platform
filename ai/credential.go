package ai

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// A model, embedding, or MCP server credential is an ordinary admin API
// token (type bearer, user or account scope) whose metadata says what it
// opens: ResourceType selects the kind, Endpoint is the base URL, and the
// tags below carry the provider and model. The sealed Token is the API key
// (static mode) or the OIDC inputs a bearer is minted from, exactly as for
// every other vault credential. @bboxsys/ui mirrors these names in
// src/lib/aiCredential.ts; keep the two in step.
const (
	// ResourceTypeModel marks a chat model credential.
	ResourceTypeModel = "ai_model"
	// ResourceTypeEmbedding marks an embeddings credential.
	ResourceTypeEmbedding = "ai_embedding"
	// ResourceTypeMCPServer marks a remote MCP server the agent may use.
	ResourceTypeMCPServer = "mcp_server"

	// TagProvider names the API dialect: anthropic, openai_compat, voyage,
	// ollama.
	TagProvider = "ai_provider"
	// TagModel is the model id, for example claude-opus-5-5 or voyage-3.
	TagModel = "ai_model"
	// TagContextWindow is the model's context window in tokens, when the
	// provider cannot discover it; the ollama and openai_compat providers
	// report it from a credential test when the server does not say.
	TagContextWindow = "ai_context_window"
	// TagMaxOutput is the maximum output tokens per turn.
	TagMaxOutput = "ai_max_output"
	// TagDimensions is the embedding vector size.
	TagDimensions = "ai_embedding_dimensions"
	// TagEffort is the reasoning effort for a model credential: low,
	// medium, high, xhigh, max.
	TagEffort = "ai_effort"
	// TagMCPTrustReadOnly, set to "true" on an mcp_server credential, makes
	// the host trust the server's readOnlyHint annotations, so its
	// read-only tools run without write approval. Absent, every tool of
	// the server is treated as a write. Set it only for a server the
	// operator controls.
	TagMCPTrustReadOnly = "mcp_trust_read_only"

	ProviderAnthropic    = "anthropic"
	ProviderOpenAICompat = "openai_compat"
	ProviderVoyage       = "voyage"
	ProviderOllama       = "ollama"

	// OllamaDefaultEndpoint is the Ollama server an ollama credential
	// without an endpoint talks to.
	OllamaDefaultEndpoint = "http://localhost:11434"
)

// OllamaBaseURL returns the Ollama server root for a credential's endpoint:
// OllamaDefaultEndpoint when it is empty, otherwise the endpoint without a
// trailing slash and without a trailing /api or /v1, since Ollama's native
// API and its OpenAI-compatible one hang off the same root and a credential
// may name either. The model provider appends /v1, the embedder /api.
func OllamaBaseURL(endpoint string) string {
	root := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if root == "" {
		return OllamaDefaultEndpoint
	}
	for _, suffix := range []string{"/api", "/v1"} {
		if strings.HasSuffix(root, suffix) {
			root = strings.TrimRight(strings.TrimSuffix(root, suffix), "/")
			break
		}
	}
	return root
}

// Credential is a resolved vault credential: metadata plus the bearer the
// host minted or unsealed for this call. It is never serialized to a
// client.
type Credential struct {
	// Name is the token's name in the vault.
	Name string
	// ResourceType is one of the ResourceType constants.
	ResourceType string
	// Provider is the TagProvider value; empty means the host's default
	// for the resource type.
	Provider string
	// Endpoint is the base URL; empty means the provider's public API.
	Endpoint string
	// Model is the TagModel value; empty means the provider's default.
	Model string
	// Bearer is the API key or minted token.
	Bearer string
	// Tags are the token's metadata tags, for provider-specific settings.
	Tags map[string]string
}

// Tag returns a tag value or "".
func (c Credential) Tag(key string) string {
	if c.Tags == nil {
		return ""
	}
	return c.Tags[key]
}

// IntTag returns a tag parsed as an integer, or def when absent or invalid.
func (c Credential) IntTag(key string, def int) int {
	v := c.Tag(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// ErrNoCredential is returned when no usable credential of the requested
// kind is visible to the caller.
var ErrNoCredential = errors.New("ai: no credential configured")

// CredentialResolver reads credentials for a caller. The host process
// that holds the admin store implements it (root admin package); the
// lookup order is the vault's: the user's own tokens first, then the
// account's shared ones. name "" means the first visible credential of
// that kind.
type CredentialResolver interface {
	Model(ctx context.Context, accountID, userID, name string) (Credential, error)
	Embedding(ctx context.Context, accountID, userID, name string) (Credential, error)
	// MCPServers returns every remote MCP server credential the caller may use.
	MCPServers(ctx context.Context, accountID, userID string) ([]Credential, error)
}

// CredentialLister is implemented by a CredentialResolver that can list
// every model credential the caller may use, in the order Model("") would
// try them, so a client can offer a choice of model per session (Status
// reports them). Without it, Status lists only the default.
type CredentialLister interface {
	Models(ctx context.Context, accountID, userID string) ([]Credential, error)
}
