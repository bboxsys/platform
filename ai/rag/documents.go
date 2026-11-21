package rag

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"sync"
	"unicode/utf8"

	"github.com/philippgille/chromem-go"
)

// Metadata keys Add sets on every stored chunk.
const (
	// MetaSourceID is the Document.ID a chunk came from.
	MetaSourceID = "source_id"
	// MetaChunk is the chunk's index within its source, "0" for the first.
	MetaChunk = "chunk"
	// MetaEmbedDim is the length of the stored embedding. Query only
	// considers documents whose dimension matches the caller's embedder,
	// so a collection holding vectors from an earlier embedding model
	// returns no results for them instead of a length-mismatch error.
	MetaEmbedDim = "embed_dim"
)

// ChunkSize is the rune length above which Add splits a document, and
// the size of the chunks it makes; ChunkOverlap is the shared context
// between consecutive chunks.
const (
	ChunkSize    = 1500
	ChunkOverlap = 200
)

// Document is a piece of text to index. Metadata is stored on every chunk
// and can be used as a Query filter; the keys source_id, chunk, and
// embed_dim are reserved for Add.
type Document struct {
	ID       string
	Content  string
	Metadata map[string]string
}

// Add indexes docs in col. Content longer than ChunkSize runes is split
// with Chunk into documents "{id}#{n}"; every stored document carries
// source_id (the Document.ID), chunk (its index), and embed_dim (its
// vector length) in metadata so Delete can remove the whole source.
// Adding a document whose id is already indexed replaces its earlier
// chunks. Every chunk is embedded with col's embedder (runtime.NumCPU
// concurrency) before anything is changed, so a failed embedding leaves
// the previously indexed version intact.
func Add(ctx context.Context, col *Collection, docs []Document) error {
	if col == nil || col.Collection == nil {
		return errors.New("rag: collection is nil")
	}
	var out []chromem.Document
	for _, d := range docs {
		if d.ID == "" {
			return errors.New("rag: document id is empty")
		}
		if d.Content == "" {
			return fmt.Errorf("rag: document %q has no content", d.ID)
		}
		var chunks []string
		if utf8.RuneCountInString(d.Content) > ChunkSize {
			chunks = Chunk(d.Content, ChunkSize, ChunkOverlap)
		} else {
			chunks = []string{d.Content}
		}
		for n, c := range chunks {
			meta := make(map[string]string, len(d.Metadata)+3)
			for k, v := range d.Metadata {
				meta[k] = v
			}
			meta[MetaSourceID] = d.ID
			meta[MetaChunk] = strconv.Itoa(n)
			id := d.ID
			if len(chunks) > 1 {
				id = d.ID + "#" + strconv.Itoa(n)
			}
			out = append(out, chromem.Document{ID: id, Content: c, Metadata: meta})
		}
	}
	if len(out) == 0 {
		return nil
	}
	if err := embedAll(ctx, col, out); err != nil {
		return err
	}
	// Replace earlier chunks of the same sources; a shorter re-index would
	// otherwise leave stale tail chunks behind. This runs only after every
	// new chunk has its embedding.
	for _, d := range docs {
		if err := Delete(ctx, col, d.ID); err != nil {
			return err
		}
	}
	if err := col.AddDocuments(ctx, out, runtime.NumCPU()); err != nil {
		return fmt.Errorf("rag: add documents: %w", err)
	}
	return nil
}

// embedAll fills Embedding and the embed_dim metadata of every document
// with col's embedder, runtime.NumCPU at a time, and returns the first
// error.
func embedAll(ctx context.Context, col *Collection, docs []chromem.Document) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	sem := make(chan struct{}, runtime.NumCPU())
	for i := range docs {
		wg.Add(1)
		sem <- struct{}{}
		go func(d *chromem.Document) {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			v, err := col.Embed(ctx, d.Content)
			if err != nil {
				once.Do(func() {
					firstErr = fmt.Errorf("rag: document %q: %w", d.ID, err)
					cancel()
				})
				return
			}
			d.Embedding = v
			d.Metadata[MetaEmbedDim] = strconv.Itoa(len(v))
		}(&docs[i])
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

// Result is one query hit.
type Result struct {
	// ID is the stored document id ("{source}#{n}" for a chunk).
	ID       string
	Content  string
	Metadata map[string]string
	// Similarity is the cosine similarity to the query, in [-1, 1].
	Similarity float32
}

// Query returns the n documents in col most similar to text, best first,
// optionally restricted to documents whose metadata matches every entry
// of where. The query is embedded with col's embedder, and only documents
// stored with the same embedding dimension (embed_dim) are considered, so
// vectors from a different embedding model are skipped rather than
// compared. It returns nil for an empty collection and clamps n to the
// collection size, since chromem rejects nResults above the document
// count.
func Query(ctx context.Context, col *Collection, text string, n int, where map[string]string) ([]Result, error) {
	if col == nil || col.Collection == nil {
		return nil, errors.New("rag: collection is nil")
	}
	if text == "" {
		return nil, errors.New("rag: query text is empty")
	}
	if n <= 0 {
		return nil, fmt.Errorf("rag: n must be positive, got %d", n)
	}
	count := col.Count()
	if count == 0 {
		return nil, nil
	}
	if n > count {
		n = count
	}
	vec, err := col.Embed(ctx, text)
	if err != nil {
		return nil, err
	}
	filter := make(map[string]string, len(where)+1)
	for k, v := range where {
		filter[k] = v
	}
	filter[MetaEmbedDim] = strconv.Itoa(len(vec))
	hits, err := col.QueryEmbedding(ctx, vec, n, filter, nil)
	if err != nil {
		return nil, fmt.Errorf("rag: query: %w", err)
	}
	out := make([]Result, 0, len(hits))
	for _, h := range hits {
		meta := make(map[string]string, len(h.Metadata))
		for k, v := range h.Metadata {
			meta[k] = v
		}
		out = append(out, Result{ID: h.ID, Content: h.Content, Metadata: meta, Similarity: h.Similarity})
	}
	return out, nil
}

// Delete removes every chunk of sourceID from col. A source that is not
// indexed is not an error.
func Delete(ctx context.Context, col *Collection, sourceID string) error {
	if col == nil || col.Collection == nil {
		return errors.New("rag: collection is nil")
	}
	if sourceID == "" {
		return errors.New("rag: source id is empty")
	}
	if col.Count() == 0 {
		return nil
	}
	if err := col.Delete(ctx, map[string]string{MetaSourceID: sourceID}, nil); err != nil {
		return fmt.Errorf("rag: delete %q: %w", sourceID, err)
	}
	return nil
}
