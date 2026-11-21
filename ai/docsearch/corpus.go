// Package docsearch gives an assistant the product's documentation: the
// sections of a docsite.Site as two read-only tools, {prefix}docs_search
// and {prefix}docs_read, and the system prompt guidance that tells the
// model to look there first and cite what it used.
//
// Search is keyword retrieval (docsite.Site.Search, BM25) from the moment
// the process starts, with no credential. With a Dir and an embedding
// credential a Corpus also keeps a vector index of the same sections and
// fuses the two rankings. The vectors live in a generation directory
// named by the embedding profile (provider, endpoint, model), so a change
// of model, even to one with the same dimension, is a new generation and
// never mixes with the old; the manifest beside the index records each
// section's content hash, so a restart or a rebuilt site re-embeds only
// what changed. Embedding uses the credential the host resolves for the
// corpus, for indexing and for queries alike, never the caller's own, so
// query and document vectors always come from the same model.
//
// The host owns the Corpus: it builds one from its loaded site, starts
// it, hands it to ai/instance through Options.Docs, and closes it after
// the instance.
package docsearch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bboxsys/platform/ai"
	"github.com/bboxsys/platform/ai/rag"
	"github.com/bboxsys/platform/docsite"
	"github.com/bboxsys/platform/logger"
	"github.com/philippgille/chromem-go"
)

const (
	// DefaultSyncInterval is how often Start re-checks the embedding
	// credential and the index when nothing is pending.
	DefaultSyncInterval = 10 * time.Minute
	// SyncBatch is how many sections one embedding round indexes before
	// the manifest is written, so an interrupted sync resumes there.
	SyncBatch = 16
	// Purpose is the rag collection purpose of a corpus index. The
	// collection's owner slot carries the project id: the index is
	// dedicated to the corpus and holds no tenant data.
	Purpose = "docs"
	// ManifestName is the file beside a generation's index.
	ManifestName = "manifest.json"
)

// Options configure New.
type Options struct {
	// Site is the loaded documentation. Required.
	Site *docsite.Site
	// SiteURL is where the site is served, as the browser reaches it:
	// absolute ("https://host:9680/docs"), because the console that
	// shows the assistant may be on another origin than the product.
	// Required.
	SiteURL string
	// Dir holds the vector generations ({Dir}/{profile}/). Empty keeps the
	// corpus keyword-only.
	Dir string
	// Embedding resolves the corpus's embedding credential, for example
	// the admin account's default. ai.ErrNoCredential means none is
	// configured yet; Start tries again at the next interval. Nil keeps the
	// corpus keyword-only.
	Embedding func(ctx context.Context) (ai.Credential, error)
	// SyncInterval is the background sync period; zero means
	// DefaultSyncInterval.
	SyncInterval time.Duration
	// Logger receives operational lines; nil logs as docsearch/{project}.
	Logger *logger.Logger
}

// Corpus is one documentation set available to an assistant.
type Corpus struct {
	opts Options
	site *docsite.Site
	log  *logger.Logger

	mu     sync.Mutex
	gen    *generation
	status VectorStatus

	runMu     sync.Mutex
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
}

// Status describes a corpus.
type Status struct {
	Project  string       `json:"project"`
	Title    string       `json:"title"`
	Revision string       `json:"revision"`
	SiteURL  string       `json:"site_url"`
	Pages    int          `json:"pages"`
	Sections int          `json:"sections"`
	Vectors  VectorStatus `json:"vectors"`
}

// VectorStatus describes the vector index.
type VectorStatus struct {
	// Enabled is whether Dir and Embedding were configured.
	Enabled bool `json:"enabled"`
	// Ready is whether the active generation covers the site revision.
	Ready bool `json:"ready"`
	// Profile names the embedding provider and model of the active
	// generation.
	Profile string `json:"profile,omitempty"`
	// Indexed is how many sections the active generation holds.
	Indexed int `json:"indexed"`
	// Pending is how many sections still need embedding.
	Pending int `json:"pending"`
	// LastSync is when a sync last finished, successfully or not.
	LastSync time.Time `json:"last_sync,omitempty"`
	// Error is the last sync's failure, or why vectors are unavailable.
	Error string `json:"error,omitempty"`
}

// New builds a corpus over o.Site. It opens nothing; Sync or Start do.
func New(o Options) (*Corpus, error) {
	if o.Site == nil {
		return nil, errors.New("docsearch: Site is required")
	}
	if o.SiteURL == "" {
		return nil, errors.New("docsearch: SiteURL is required")
	}
	if u, err := url.Parse(o.SiteURL); err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("docsearch: SiteURL %q must be absolute", o.SiteURL)
	}
	o.SiteURL = strings.TrimRight(o.SiteURL, "/")
	if o.SyncInterval <= 0 {
		o.SyncInterval = DefaultSyncInterval
	}
	if o.Logger == nil {
		o.Logger = logger.NewLogger("docsearch", o.Site.Project)
	}
	c := &Corpus{opts: o, site: o.Site, log: o.Logger}
	c.status.Enabled = o.Dir != "" && o.Embedding != nil
	if !c.status.Enabled {
		c.status.Error = "vectors not configured; keyword search only"
	}
	return c, nil
}

// Site returns the documentation.
func (c *Corpus) Site() *docsite.Site { return c.site }

// SiteURL returns where the site is served.
func (c *Corpus) SiteURL() string { return c.opts.SiteURL }

// Status reports the corpus and its vector index.
func (c *Corpus) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Status{
		Project: c.site.Project, Title: c.site.Title, Revision: c.site.Revision, SiteURL: c.opts.SiteURL,
		Pages: len(c.site.Pages), Sections: len(c.site.Sections), Vectors: c.status,
	}
}

// Start runs Sync now and then every SyncInterval until Close, so an
// embedding credential added later is picked up and a failed round is
// retried. It returns at once.
func (c *Corpus) Start() {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	if c.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.done = make(chan struct{})
	go func() {
		defer close(c.done)
		t := time.NewTicker(c.opts.SyncInterval)
		defer t.Stop()
		for {
			if err := c.Sync(ctx); err != nil && ctx.Err() == nil {
				c.log.WarnE().Err(err).Msg("docsearch: sync failed; will retry")
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// Close stops the background sync, waits for it, and closes the index.
func (c *Corpus) Close() error {
	c.closeOnce.Do(func() {
		c.runMu.Lock()
		cancel, done := c.cancel, c.done
		c.runMu.Unlock()
		if cancel != nil {
			cancel()
			<-done
		}
		c.mu.Lock()
		if c.gen != nil {
			c.gen.index.Close()
			c.gen = nil
		}
		c.mu.Unlock()
	})
	return nil
}

// manifest records what a generation holds.
type manifest struct {
	Profile  string            `json:"profile"`
	Provider string            `json:"provider"`
	Model    string            `json:"model"`
	Revision string            `json:"revision"`
	Sections map[string]string `json:"sections"`
}

// generation is one vector index built with one embedding profile.
type generation struct {
	key      string
	profile  string
	dir      string
	index    *rag.Index
	col      *rag.Collection
	manifest manifest
}

// Sync brings the vector index up to date with the site in one pass:
// resolves the credential, opens (or switches to) the generation for its
// profile, embeds changed and new sections in batches, removes sections
// the site no longer has, and retires other generations once this one is
// complete. Without a credential it records that and returns nil.
func (c *Corpus) Sync(ctx context.Context) error {
	if !c.status.Enabled {
		return nil
	}
	finish := func(err error) error {
		c.mu.Lock()
		c.status.LastSync = time.Now()
		if err != nil {
			c.status.Error = err.Error()
		} else {
			c.status.Error = ""
		}
		c.mu.Unlock()
		return err
	}
	cred, err := c.opts.Embedding(ctx)
	if err != nil {
		if errors.Is(err, ai.ErrNoCredential) {
			c.mu.Lock()
			c.status.LastSync = time.Now()
			c.status.Error = "no embedding credential configured; keyword search only"
			c.status.Ready = false
			c.mu.Unlock()
			return nil
		}
		return finish(fmt.Errorf("embedding credential: %w", err))
	}
	embed, err := rag.Embedder(cred)
	if err != nil {
		return finish(err)
	}
	gen, err := c.generation(cred, embed)
	if err != nil {
		return finish(err)
	}

	// What the site has, by section id, against what the generation holds.
	want := map[string]*docsite.Section{}
	for _, sec := range c.site.Sections {
		if strings.TrimSpace(sec.Text) == "" {
			continue
		}
		want[sec.ID] = sec
	}
	var stale []string
	for id := range gen.manifest.Sections {
		if _, ok := want[id]; !ok {
			stale = append(stale, id)
		}
	}
	var todo []*docsite.Section
	for _, sec := range c.site.Sections {
		w := want[sec.ID]
		if w != nil && gen.manifest.Sections[sec.ID] != sec.Hash {
			todo = append(todo, sec)
		}
	}
	c.setProgress(gen, len(todo))
	if len(stale) == 0 && len(todo) == 0 && gen.manifest.Revision == c.site.Revision {
		c.setReady(gen, true)
		return finish(nil)
	}
	c.log.InfoE().Str("profile", gen.profile).Int("pending", len(todo)).Int("stale", len(stale)).Msg("docsearch: syncing vectors")

	sort.Strings(stale)
	for _, id := range stale {
		if err := ctx.Err(); err != nil {
			return finish(err)
		}
		if err := rag.Delete(ctx, gen.col, id); err != nil {
			return finish(err)
		}
		delete(gen.manifest.Sections, id)
	}
	if err := c.writeManifest(gen); err != nil {
		return finish(err)
	}
	for len(todo) > 0 {
		if err := ctx.Err(); err != nil {
			return finish(err)
		}
		n := SyncBatch
		if n > len(todo) {
			n = len(todo)
		}
		batch, rest := todo[:n], todo[n:]
		docs := make([]rag.Document, 0, len(batch))
		for _, sec := range batch {
			docs = append(docs, rag.Document{
				ID:       sec.ID,
				Content:  sec.HeadingPath() + "\n\n" + sec.Text,
				Metadata: map[string]string{"page": sec.Page, "title": sec.Title},
			})
		}
		if err := rag.Add(ctx, gen.col, docs); err != nil {
			return finish(err)
		}
		for _, sec := range batch {
			gen.manifest.Sections[sec.ID] = sec.Hash
		}
		todo = rest
		if err := c.writeManifest(gen); err != nil {
			return finish(err)
		}
		c.setProgress(gen, len(todo))
	}
	gen.manifest.Revision = c.site.Revision
	if err := c.writeManifest(gen); err != nil {
		return finish(err)
	}
	c.setReady(gen, true)
	c.retire(gen)
	c.log.InfoE().Str("profile", gen.profile).Int("sections", len(gen.manifest.Sections)).Msg("docsearch: vectors ready")
	return finish(nil)
}

// generation returns the open generation for the credential's profile,
// opening or switching as needed, with a collection handle bound to the
// current embedding function (so a rotated key is used at once).
func (c *Corpus) generation(cred ai.Credential, embed chromem.EmbeddingFunc) (*generation, error) {
	profile := profileName(cred)
	key := profileKey(profile)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != nil && c.gen.key != key {
		c.log.InfoE().Str("from", c.gen.profile).Str("to", profile).Msg("docsearch: embedding profile changed; switching generation")
		c.gen.index.Close()
		c.gen = nil
	}
	if c.gen == nil {
		dir := filepath.Join(c.opts.Dir, key)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("docsearch: create %s: %w", dir, err)
		}
		index, err := rag.Open(dir)
		if err != nil {
			return nil, err
		}
		g := &generation{key: key, profile: profile, dir: dir, index: index}
		g.manifest = manifest{Profile: profile, Provider: cred.Provider, Model: cred.Model, Sections: map[string]string{}}
		if data, err := os.ReadFile(filepath.Join(dir, ManifestName)); err == nil {
			var m manifest
			if err := json.Unmarshal(data, &m); err == nil && m.Profile == profile {
				if m.Sections == nil {
					m.Sections = map[string]string{}
				}
				g.manifest = m
			}
		}
		c.gen = g
		c.status.Profile = profile
		c.status.Indexed = len(g.manifest.Sections)
		c.status.Ready = false
	}
	col, err := c.gen.index.Collection(c.site.Project, Purpose, embed)
	if err != nil {
		return nil, err
	}
	c.gen.col = col
	return c.gen, nil
}

func (c *Corpus) setProgress(g *generation, pending int) {
	c.mu.Lock()
	c.status.Indexed = len(g.manifest.Sections)
	c.status.Pending = pending
	c.mu.Unlock()
}

func (c *Corpus) setReady(g *generation, ready bool) {
	c.mu.Lock()
	c.status.Ready = ready
	c.status.Indexed = len(g.manifest.Sections)
	c.status.Pending = 0
	c.mu.Unlock()
}

func (c *Corpus) writeManifest(g *generation) error {
	data, err := json.MarshalIndent(g.manifest, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(g.dir, ManifestName+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("docsearch: write manifest: %w", err)
	}
	return os.Rename(tmp, filepath.Join(g.dir, ManifestName))
}

// retire removes the generations other than g, which is complete.
func (c *Corpus) retire(g *generation) {
	entries, err := os.ReadDir(c.opts.Dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == g.key {
			continue
		}
		if err := os.RemoveAll(filepath.Join(c.opts.Dir, e.Name())); err != nil {
			c.log.WarnE().Err(err).Str("generation", e.Name()).Msg("docsearch: retire generation")
		}
	}
}

// profileName names an embedding profile: provider, endpoint host and
// model, which together decide the vector space.
func profileName(cred ai.Credential) string {
	host := ""
	if cred.Endpoint != "" {
		if u, err := url.Parse(cred.Endpoint); err == nil {
			host = u.Host
		}
	}
	model := cred.Model
	if model == "" {
		switch cred.Provider {
		case ai.ProviderVoyage:
			model = rag.VoyageDefaultModel
		case ai.ProviderOllama:
			model = rag.OllamaDefaultModel
		}
	}
	parts := []string{cred.Provider}
	if host != "" {
		parts = append(parts, host)
	}
	parts = append(parts, model)
	return strings.Join(parts, "/")
}

func profileKey(profile string) string {
	sum := sha256.Sum256([]byte(profile))
	return hex.EncodeToString(sum[:])[:12]
}

// Hit is one search result.
type Hit struct {
	Section *docsite.Section
	Snippet string
	// Score is the fused rank score; it orders hits and means nothing on
	// its own.
	Score float64
}

const rrfK = 60.0

// Search ranks sections for query: the keyword ranking fused (reciprocal
// rank) with the vector ranking when the index has vectors. It returns at
// most n hits.
func (c *Corpus) Search(ctx context.Context, query string, n int) ([]Hit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("docsearch: query is empty")
	}
	if n <= 0 {
		n = 5
	}
	keyword := c.site.Search(query, n*3)
	scores := map[string]float64{}
	snippets := map[string]string{}
	for i, h := range keyword {
		scores[h.Section.ID] += 1 / (rrfK + float64(i+1))
		snippets[h.Section.ID] = h.Snippet
	}
	c.mu.Lock()
	gen := c.gen
	c.mu.Unlock()
	if gen != nil && gen.col != nil && len(gen.manifest.Sections) > 0 {
		hits, err := rag.Query(ctx, gen.col, query, n*3, nil)
		if err != nil {
			// Vectors are an improvement, not a dependency: log and answer
			// from keywords.
			c.log.WarnE().Err(err).Msg("docsearch: vector query failed; keyword results only")
		}
		rank := 0
		seen := map[string]bool{}
		for _, h := range hits {
			id := h.Metadata[rag.MetaSourceID]
			if id == "" || seen[id] || c.site.Section(id) == nil {
				continue
			}
			seen[id] = true
			rank++
			scores[id] += 1 / (rrfK + float64(rank))
		}
	}
	ids := make([]string, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(a, b int) bool {
		if scores[ids[a]] != scores[ids[b]] {
			return scores[ids[a]] > scores[ids[b]]
		}
		return ids[a] < ids[b]
	})
	if len(ids) > n {
		ids = ids[:n]
	}
	out := make([]Hit, 0, len(ids))
	for _, id := range ids {
		sec := c.site.Section(id)
		snip := snippets[id]
		if snip == "" {
			snip = docsite.Excerpt(sec.Text, query)
		}
		out = append(out, Hit{Section: sec, Snippet: snip, Score: scores[id]})
	}
	return out, nil
}
