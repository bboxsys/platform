// Package rag is retrieval over an embedded vector store. An Index is a
// persistent chromem-go database under the host's store directory; every
// collection in it belongs to one account and one purpose
// ("documents:{accountID}", "memory:{accountID}"), so a query can never
// cross tenants. Embeddings come from an embedding credential in the admin
// vault (Voyage, any OpenAI-compatible endpoint, or Ollama); Anthropic has
// no embeddings endpoint, so a model credential cannot be used here.
//
// chromem-go is exhaustive cosine search in pure Go with one gob file per
// document on disk. That is the intended scale: a tenant's runbooks,
// configuration exports, and notes, not a corpus.
package rag

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/bboxsys/platform/ai"
	"github.com/philippgille/chromem-go"
)

// Index is an open vector database holding per-account collections.
type Index struct {
	mu     sync.Mutex
	db     *chromem.DB
	closed bool
}

// ErrClosed is returned by Collection after Close.
var ErrClosed = errors.New("rag: index is closed")

// Open opens (or creates) the persistent database under dir. Files are
// gzip-compressed gob; writes are synchronous, so there is nothing to
// flush on Close.
func Open(dir string) (*Index, error) {
	if dir == "" {
		return nil, errors.New("rag: directory is empty")
	}
	db, err := chromem.NewPersistentDB(dir, true)
	if err != nil {
		return nil, fmt.Errorf("rag: open %s: %w", dir, err)
	}
	return &Index{db: db}, nil
}

// Close releases the index. chromem-go persists every write as it happens,
// so Close only marks the index unusable; it never fails today but returns
// an error for callers that treat it like any other closer.
func (ix *Index) Close() error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.closed = true
	ix.db = nil
	return nil
}

// CollectionName is the name Collection uses for an account and purpose.
func CollectionName(accountID, purpose string) string {
	return purpose + ":" + accountID
}

// Collection is one account collection bound to the embedding function of
// the caller that opened it. The underlying chromem collection is shared
// by every caller in the account, but its own embedding function is a
// placeholder that always fails: Add, Query, and the memory package embed
// with the function held here, so each caller's credential is used for
// that caller's operations only, and rotating or deleting a credential
// takes effect on the next call. The chromem methods are promoted for
// reads (Count, GetByID) and deletes; calling a chromem method that would
// embed implicitly (AddDocument without an Embedding, Query by text) fails
// with ErrNoEmbedder instead of silently using someone else's key.
type Collection struct {
	*chromem.Collection
	embed chromem.EmbeddingFunc
}

// Embed returns the embedding of text from the caller's embedding
// function. An empty vector is an error.
func (c *Collection) Embed(ctx context.Context, text string) ([]float32, error) {
	if c == nil || c.Collection == nil {
		return nil, errors.New("rag: collection is nil")
	}
	if c.embed == nil {
		return nil, ErrNoEmbedder
	}
	v, err := c.embed(ctx, text)
	if err != nil {
		return nil, fmt.Errorf("rag: embed: %w", err)
	}
	if len(v) == 0 {
		return nil, errors.New("rag: embedding function returned an empty vector")
	}
	return v, nil
}

// ErrNoEmbedder is what the placeholder embedding function of every
// collection returns, so an implicit chromem embed fails loudly.
var ErrNoEmbedder = errors.New("rag: collection has no embedder; pass embeddings explicitly")

// noEmbedder is the embedding function chromem holds for every collection.
func noEmbedder(context.Context, string) ([]float32, error) {
	return nil, ErrNoEmbedder
}

// Collection returns the account's collection for purpose, creating it on
// first use, bound to embed. The name is "{purpose}:{accountID}". purpose
// must not contain a colon so the name stays unambiguous; accountID is the
// admin account id. embed is never stored on the shared chromem
// collection (see Collection); it is used only through the returned
// handle, so open a handle per caller and per request.
//
// The call is serialized because chromem's GetOrCreateCollection is not
// atomic: two concurrent creators would each build an empty collection and
// the second would replace the first in the database's map.
func (ix *Index) Collection(accountID, purpose string, embed chromem.EmbeddingFunc) (*Collection, error) {
	if accountID == "" || strings.Contains(accountID, ":") {
		return nil, fmt.Errorf("rag: invalid account id %q", accountID)
	}
	if purpose == "" || strings.Contains(purpose, ":") {
		return nil, fmt.Errorf("rag: invalid purpose %q", purpose)
	}
	if embed == nil {
		return nil, errors.New("rag: embedding function is nil")
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.closed || ix.db == nil {
		return nil, ErrClosed
	}
	col, err := ix.db.GetOrCreateCollection(CollectionName(accountID, purpose), map[string]string{
		"account_id": accountID,
		"purpose":    purpose,
	}, noEmbedder)
	if err != nil {
		return nil, fmt.Errorf("rag: collection %s for account %s: %w", purpose, accountID, err)
	}
	return &Collection{Collection: col, embed: embed}, nil
}

// Default endpoints and models for the embedding providers.
const (
	VoyageEndpoint     = "https://api.voyageai.com/v1"
	VoyageDefaultModel = "voyage-3"
	OllamaEndpoint     = ai.OllamaDefaultEndpoint + "/api"
	OllamaDefaultModel = "nomic-embed-text"
)

// Embedder builds the embedding function for an embedding credential,
// chosen by cred.Provider:
//
//   - ai.ProviderVoyage: Voyage's OpenAI-compatible endpoint
//     (VoyageEndpoint unless cred.Endpoint is set), model VoyageDefaultModel
//     unless cred.Model is set; the bearer is the Voyage API key.
//   - ai.ProviderOpenAICompat: any OpenAI-compatible /embeddings endpoint;
//     cred.Endpoint and cred.Model are required, the bearer is optional for
//     local servers.
//   - ai.ProviderOllama: Ollama's native API under the server root
//     (ai.OllamaBaseURL: OllamaEndpoint unless cred.Endpoint is set; a
//     trailing /api or /v1 is accepted), model OllamaDefaultModel unless set.
//
// Anthropic has no embeddings endpoint, so ai.ProviderAnthropic and any
// other provider are an error. Vectors are normalized by chromem when the
// provider does not already return unit vectors.
func Embedder(cred ai.Credential) (chromem.EmbeddingFunc, error) {
	if cred.ResourceType != "" && cred.ResourceType != ai.ResourceTypeEmbedding {
		return nil, fmt.Errorf("rag: credential %q is %s, not %s", cred.Name, cred.ResourceType, ai.ResourceTypeEmbedding)
	}
	endpoint := strings.TrimRight(cred.Endpoint, "/")
	switch cred.Provider {
	case ai.ProviderVoyage:
		if cred.Bearer == "" {
			return nil, fmt.Errorf("rag: credential %q has no Voyage API key", cred.Name)
		}
		if endpoint == "" {
			endpoint = VoyageEndpoint
		}
		model := cred.Model
		if model == "" {
			model = VoyageDefaultModel
		}
		return chromem.NewEmbeddingFuncOpenAICompat(endpoint, cred.Bearer, model, nil), nil
	case ai.ProviderOpenAICompat:
		if endpoint == "" {
			return nil, fmt.Errorf("rag: credential %q needs an endpoint for an OpenAI-compatible embedding API", cred.Name)
		}
		if cred.Model == "" {
			return nil, fmt.Errorf("rag: credential %q needs a model tag (%s)", cred.Name, ai.TagModel)
		}
		return chromem.NewEmbeddingFuncOpenAICompat(endpoint, cred.Bearer, cred.Model, nil), nil
	case ai.ProviderOllama:
		endpoint = ai.OllamaBaseURL(cred.Endpoint) + "/api"
		model := cred.Model
		if model == "" {
			model = OllamaDefaultModel
		}
		return chromem.NewEmbeddingFuncOllama(model, endpoint), nil
	case ai.ProviderAnthropic:
		return nil, fmt.Errorf("rag: credential %q: Anthropic has no embeddings API; use a Voyage, OpenAI-compatible, or Ollama credential", cred.Name)
	case "":
		return nil, fmt.Errorf("rag: credential %q has no provider tag (%s)", cred.Name, ai.TagProvider)
	default:
		return nil, fmt.Errorf("rag: credential %q: unknown embedding provider %q", cred.Name, cred.Provider)
	}
}
