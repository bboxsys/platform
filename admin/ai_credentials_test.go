package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/bboxsys/platform/ai"
	"github.com/stretchr/testify/require"
)

func putAICredential(t *testing.T, st *Store, accountID, userID, name string, scope APITokenScope, resourceType, secret string, tags map[string]string) {
	t.Helper()
	token := NewAPIToken(APIToken{
		Name:  name,
		Type:  APITokenTypeBearer,
		Scope: scope,
		Token: secret,
		Metadata: APITokenMetadata{
			ResourceType: resourceType,
			Endpoint:     "https://" + name + ".example",
			Tags:         tags,
		},
	}, userID)
	require.NoError(t, st.PutAPIToken(accountID, token))
}

func TestAICredentialResolver(t *testing.T) {
	st := newTestStore(t)
	_, account := bootstrapAccount(t, st, "tenant")
	const alice = "UALICE"
	const bob = "UBOB"
	ctx := context.Background()
	r := NewAICredentialResolver(st)

	// Nothing configured yet.
	_, err := r.Model(ctx, account.ID, alice, "")
	require.ErrorIs(t, err, ai.ErrNoCredential)

	// An account-wide model, created by bob, visible to alice.
	putAICredential(t, st, account.ID, bob, "shared-model", APITokenScopeAccount, ai.ResourceTypeModel, "sk-shared",
		map[string]string{ai.TagProvider: ai.ProviderAnthropic, ai.TagModel: "claude-opus-5-5", ai.TagEffort: "high"})
	cred, err := r.Model(ctx, account.ID, alice, "")
	require.NoError(t, err)
	require.Equal(t, "shared-model", cred.Name)
	require.Equal(t, "sk-shared", cred.Bearer)
	require.Equal(t, ai.ProviderAnthropic, cred.Provider)
	require.Equal(t, "claude-opus-5-5", cred.Model)
	require.Equal(t, "https://shared-model.example", cred.Endpoint)
	require.Equal(t, "high", cred.Tag(ai.TagEffort))

	// Alice's own model wins over the account default; bob's private one
	// is invisible to her.
	putAICredential(t, st, account.ID, alice, "my-model", APITokenScopeUser, ai.ResourceTypeModel, "sk-alice", map[string]string{ai.TagProvider: ai.ProviderAnthropic})
	putAICredential(t, st, account.ID, bob, "bob-model", APITokenScopeUser, ai.ResourceTypeModel, "sk-bob", nil)
	cred, err = r.Model(ctx, account.ID, alice, "")
	require.NoError(t, err)
	require.Equal(t, "my-model", cred.Name)
	cred, err = r.Model(ctx, account.ID, alice, "shared-model")
	require.NoError(t, err)
	require.Equal(t, "sk-shared", cred.Bearer)
	_, err = r.Model(ctx, account.ID, alice, "bob-model")
	require.ErrorIs(t, err, ai.ErrNoCredential)

	// Resource types do not bleed into each other.
	_, err = r.Embedding(ctx, account.ID, alice, "")
	require.ErrorIs(t, err, ai.ErrNoCredential)
	putAICredential(t, st, account.ID, bob, "embed", APITokenScopeAccount, ai.ResourceTypeEmbedding, "sk-embed",
		map[string]string{ai.TagProvider: ai.ProviderVoyage, ai.TagModel: "voyage-3", ai.TagDimensions: "1024"})
	cred, err = r.Embedding(ctx, account.ID, alice, "")
	require.NoError(t, err)
	require.Equal(t, ai.ProviderVoyage, cred.Provider)
	require.Equal(t, 1024, cred.IntTag(ai.TagDimensions, 0))

	// MCP servers: alice sees the shared one and her own, not bob's.
	putAICredential(t, st, account.ID, bob, "shared-mcp", APITokenScopeAccount, ai.ResourceTypeMCPServer, "mcp-shared", nil)
	putAICredential(t, st, account.ID, alice, "alice-mcp", APITokenScopeUser, ai.ResourceTypeMCPServer, "mcp-alice", nil)
	putAICredential(t, st, account.ID, bob, "bob-mcp", APITokenScopeUser, ai.ResourceTypeMCPServer, "mcp-bob", nil)
	servers, err := r.MCPServers(ctx, account.ID, alice)
	require.NoError(t, err)
	require.Len(t, servers, 2)
	require.Equal(t, "alice-mcp", servers[0].Name)
	require.Equal(t, "shared-mcp", servers[1].Name)

	// A nil store is reported, not dereferenced.
	var none *AICredentialResolver
	_, err = none.Model(ctx, account.ID, alice, "")
	require.Error(t, err)
	require.False(t, errors.Is(err, ai.ErrNoCredential))
}
