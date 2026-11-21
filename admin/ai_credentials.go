package admin

import (
	"context"
	"fmt"
	"sort"

	"github.com/bboxsys/platform/ai"
)

// AICredentialResolver reads model, embedding, and MCP server credentials
// from the admin API token vault for package ai. It lives here because
// only the process that hosts the admin server holds the store and can
// open a sealed secret: the bearer is read for one run and never returned
// by any route.
//
// A credential is an ordinary API token whose metadata.resource_type is
// one of ai.ResourceTypeModel, ai.ResourceTypeEmbedding, or
// ai.ResourceTypeMCPServer. Lookup order is the vault's: the user's own
// tokens before the account's shared ones, each by name, so a user can
// override an account default with a private credential of their own.
// Turning the stored secret into a bearer (static, or minted through
// OIDC) is the platform's APITokenResolver.
type AICredentialResolver struct {
	store   *Store
	bearers *APITokenResolver
}

// NewAICredentialResolver returns a resolver over the store.
func NewAICredentialResolver(store *Store) *AICredentialResolver {
	return &AICredentialResolver{store: store, bearers: NewAPITokenResolver(store)}
}

var (
	_ ai.CredentialResolver = (*AICredentialResolver)(nil)
	_ ai.CredentialLister   = (*AICredentialResolver)(nil)
)

// Model returns the model credential named name, or the first visible one
// when name is empty.
func (r *AICredentialResolver) Model(ctx context.Context, accountID, userID, name string) (ai.Credential, error) {
	return r.one(ctx, accountID, userID, ai.ResourceTypeModel, name)
}

// Embedding returns the embedding credential named name, or the first
// visible one when name is empty.
func (r *AICredentialResolver) Embedding(ctx context.Context, accountID, userID, name string) (ai.Credential, error) {
	return r.one(ctx, accountID, userID, ai.ResourceTypeEmbedding, name)
}

// Models returns every model credential the caller can see, in the order
// Model("") tries them (the user's own first, then the account's, by
// name), each with its bearer resolved. A credential whose bearer cannot
// be minted is skipped rather than failing the whole list.
func (r *AICredentialResolver) Models(ctx context.Context, accountID, userID string) ([]ai.Credential, error) {
	return r.all(ctx, accountID, userID, ai.ResourceTypeModel)
}

// MCPServers returns every MCP server credential the caller can see, each
// with its bearer resolved. A credential whose bearer cannot be minted is
// skipped rather than failing the whole list.
func (r *AICredentialResolver) MCPServers(ctx context.Context, accountID, userID string) ([]ai.Credential, error) {
	return r.all(ctx, accountID, userID, ai.ResourceTypeMCPServer)
}

func (r *AICredentialResolver) all(ctx context.Context, accountID, userID, resourceType string) ([]ai.Credential, error) {
	tokens, err := r.visible(accountID, userID, resourceType)
	if err != nil {
		return nil, err
	}
	out := make([]ai.Credential, 0, len(tokens))
	for i := range tokens {
		cred, err := r.credential(ctx, accountID, &tokens[i])
		if err != nil {
			continue
		}
		out = append(out, cred)
	}
	return out, nil
}

func (r *AICredentialResolver) one(ctx context.Context, accountID, userID, resourceType, name string) (ai.Credential, error) {
	tokens, err := r.visible(accountID, userID, resourceType)
	if err != nil {
		return ai.Credential{}, err
	}
	for i := range tokens {
		if name == "" || tokens[i].Name == name {
			return r.credential(ctx, accountID, &tokens[i])
		}
	}
	if name == "" {
		return ai.Credential{}, ai.ErrNoCredential
	}
	return ai.Credential{}, fmt.Errorf("%w: %s %q", ai.ErrNoCredential, resourceType, name)
}

// visible returns the caller's tokens of one resource type, the user's
// own first, then the account's, each sorted by name.
func (r *AICredentialResolver) visible(accountID, userID, resourceType string) ([]APIToken, error) {
	if r == nil || r.store == nil {
		return nil, fmt.Errorf("admin store is not available")
	}
	all, err := r.store.GetAPITokensVisibleTo(accountID, userID)
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	tokens := filterAPITokens(all, func(t *APIToken) bool {
		return t.Metadata.ResourceType == resourceType
	})
	sort.SliceStable(tokens, func(i, j int) bool {
		if tokens[i].Scope != tokens[j].Scope {
			return tokens[i].Scope == APITokenScopeUser
		}
		return tokens[i].Name < tokens[j].Name
	})
	return tokens, nil
}

func (r *AICredentialResolver) credential(ctx context.Context, accountID string, token *APIToken) (ai.Credential, error) {
	bearer, err := r.bearers.Bearer(ctx, accountID, token)
	if err != nil {
		return ai.Credential{}, fmt.Errorf("resolve bearer for %s: %w", token.Name, err)
	}
	tags := make(map[string]string, len(token.Metadata.Tags))
	for k, v := range token.Metadata.Tags {
		tags[k] = v
	}
	return ai.Credential{
		Name:         token.Name,
		ResourceType: token.Metadata.ResourceType,
		Provider:     tags[ai.TagProvider],
		Endpoint:     token.Metadata.Endpoint,
		Model:        tags[ai.TagModel],
		Bearer:       bearer,
		Tags:         tags,
	}, nil
}
