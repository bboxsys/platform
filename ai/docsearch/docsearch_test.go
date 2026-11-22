package docsearch

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/bboxsys/platform/ai"
	"github.com/bboxsys/platform/docsite"
)

const page = `<html><head><title>Rule of 55 | Acme</title></head><body><main>
<h1 id="_top">The Rule of 55</h1><div class="sl-markdown-content">
<p>Leaving an employer in or after the year you turn 55 lets you draw that plan without the 10 percent penalty.</p>
<h2 id="the-plan-document">The plan document</h2>
<p>The plan must allow partial withdrawals after separation. Check the summary plan description.</p>
<h2 id="mistakes">Mistakes that forfeit it</h2>
<p>Rolling the balance to an IRA ends the exception. Set ai.allow_writes only in a test.</p>
</div></main></body></html>`

const index = `<html><head><title>Acme</title></head><body><main><h1 id="_top">Acme</h1>
<div class="sl-markdown-content"><p>Research notes for a household.</p></div></main></body></html>`

func testSite(t *testing.T) *docsite.Site {
	t.Helper()
	fsys := fstest.MapFS{
		"index.html":                     {Data: []byte(index)},
		"planning/rule-of-55/index.html": {Data: []byte(page)},
	}
	s, err := docsite.Load(fsys, docsite.Options{Project: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func callTool(t *testing.T, tool ai.Tool, input string) ai.Result {
	t.Helper()
	res, err := tool.Execute(context.Background(), json.RawMessage(input))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestKeywordOnlyTools(t *testing.T) {
	c, err := New(Options{Site: testSite(t), SiteURL: "https://host:9680/docs/"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	st := c.Status()
	if st.Vectors.Enabled || st.Sections != 4 || st.Project != "acme" {
		t.Errorf("status %+v", st)
	}
	tools := c.Tools("acme_")
	if len(tools) != 2 || tools[0].Name() != "acme_docs_search" || tools[1].Name() != "acme_docs_read" || !tools[0].ReadOnly() || !tools[1].ReadOnly() {
		t.Fatalf("tools %v", tools)
	}
	res := callTool(t, tools[0], `{"query":"partial withdrawals after separation"}`)
	if res.IsError || !strings.Contains(res.Content, "id=planning/rule-of-55/#the-plan-document") || !strings.Contains(res.Content, "url=https://host:9680/docs/planning/rule-of-55/#the-plan-document") {
		t.Errorf("search: %+v", res)
	}
	if !strings.HasPrefix(res.Content, "1. id=planning/rule-of-55/#the-plan-document") {
		t.Errorf("best hit first: %s", res.Content)
	}
	res = callTool(t, tools[0], `{"query":"ai.allow_writes","n":1}`)
	if !strings.Contains(res.Content, "#mistakes") {
		t.Errorf("identifier search: %s", res.Content)
	}
	res = callTool(t, tools[0], `{"query":""}`)
	if !res.IsError {
		t.Error("empty query should be an error result")
	}
	res = callTool(t, tools[1], `{"id":"planning/rule-of-55/#mistakes"}`)
	if res.IsError || !strings.Contains(res.Content, "Rolling the balance") || !strings.Contains(res.Content, "The Rule of 55 > Mistakes that forfeit it") {
		t.Errorf("read: %+v", res)
	}
	res = callTool(t, tools[1], `{"id":"planning/rule-of-55/"}`)
	if res.IsError || !strings.Contains(res.Content, "Sections:") || !strings.Contains(res.Content, "- id=planning/rule-of-55/#the-plan-document") || !strings.Contains(res.Content, "Leaving an employer") {
		t.Errorf("read page: %+v", res)
	}
	res = callTool(t, tools[1], `{"id":"planning/rule-of-55/"}`)
	res = callTool(t, tools[1], `{"id":"nope"}`)
	if !res.IsError {
		t.Error("unknown id should be an error result")
	}
	if g := c.Guidance("acme_"); !strings.Contains(g, "acme_docs_search") || !strings.Contains(g, "Acme documentation") {
		t.Errorf("guidance %q", g)
	}
}

func TestNewValidates(t *testing.T) {
	if _, err := New(Options{Site: testSite(t)}); err == nil {
		t.Error("SiteURL required")
	}
	if _, err := New(Options{Site: testSite(t), SiteURL: "/docs"}); err == nil {
		t.Error("SiteURL must be absolute")
	}
	if _, err := New(Options{SiteURL: "https://x/docs"}); err == nil {
		t.Error("Site required")
	}
}

// embeddingServer speaks the OpenAI embeddings dialect with a
// deterministic bag-of-words vector, enough for similarity to mean
// something in a test. calls counts embedding requests; it is atomic
// because the server handles a sync's batches on concurrent goroutines.
func embeddingServer(t *testing.T, dims int, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		var inputs []string
		switch v := req.Input.(type) {
		case string:
			inputs = []string{v}
		case []any:
			for _, x := range v {
				inputs = append(inputs, x.(string))
			}
		}
		calls.Add(int64(len(inputs)))
		type item struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}
		var data []item
		for i, in := range inputs {
			vec := make([]float32, dims)
			for _, w := range strings.Fields(strings.ToLower(in)) {
				h := fnv.New32a()
				h.Write([]byte(w))
				vec[h.Sum32()%uint32(dims)] += 1
			}
			var norm float64
			for _, x := range vec {
				norm += float64(x * x)
			}
			if norm > 0 {
				for i := range vec {
					vec[i] = float32(float64(vec[i]) / math.Sqrt(norm))
				}
			}
			data = append(data, item{Embedding: vec, Index: i})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
}

func TestVectorSync(t *testing.T) {
	var calls atomic.Int64
	srv := embeddingServer(t, 64, &calls)
	defer srv.Close()
	cred := ai.Credential{Name: "emb", ResourceType: ai.ResourceTypeEmbedding, Provider: ai.ProviderOpenAICompat, Endpoint: srv.URL, Model: "test-a"}
	credential := func(ctx context.Context) (ai.Credential, error) { return cred, nil }
	dir := t.TempDir()
	site := testSite(t)
	c, err := New(Options{Site: site, SiteURL: "https://host/docs", Dir: dir, Embedding: credential})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := c.Status().Vectors
	if !st.Enabled || !st.Ready || st.Indexed != 4 || st.Pending != 0 || st.Error != "" || !strings.HasPrefix(st.Profile, "openai_compat/") {
		t.Errorf("after sync: %+v", st)
	}
	if calls.Load() != 4 {
		t.Errorf("embedded %d sections, want 4", calls.Load())
	}
	// A second sync of an unchanged site embeds nothing.
	if err := c.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 {
		t.Errorf("unchanged sync re-embedded: %d", calls.Load())
	}
	// Fused search still finds the section; the query is embedded once.
	hits, err := c.Search(context.Background(), "rolling to an IRA", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Section.ID != "planning/rule-of-55/#mistakes" {
		t.Errorf("hits %+v", hits)
	}
	if calls.Load() != 5 {
		t.Errorf("query embeddings: %d", calls.Load())
	}
	// A restart (new corpus over the same directory) reuses the manifest.
	c.Close()
	c2, err := New(Options{Site: site, SiteURL: "https://host/docs", Dir: dir, Embedding: credential})
	if err != nil {
		t.Fatal(err)
	}
	if err := c2.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 5 || !c2.Status().Vectors.Ready {
		t.Errorf("restart re-embedded: %d %+v", calls.Load(), c2.Status().Vectors)
	}
	// A changed model is a new generation; the old one is retired once
	// the new one is complete.
	cred.Model = "test-b"
	if err := c2.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 9 || !c2.Status().Vectors.Ready {
		t.Errorf("new profile: %d %+v", calls.Load(), c2.Status().Vectors)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("generations after switch: %d", len(entries))
	}
	if _, err := os.Stat(filepath.Join(dir, entries[0].Name(), ManifestName)); err != nil {
		t.Error("manifest missing")
	}
	c2.Close()
}

func TestSyncWithoutCredential(t *testing.T) {
	c, err := New(Options{Site: testSite(t), SiteURL: "https://host/docs", Dir: t.TempDir(),
		Embedding: func(context.Context) (ai.Credential, error) { return ai.Credential{}, ai.ErrNoCredential }})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := c.Status().Vectors
	if !st.Enabled || st.Ready || !strings.Contains(st.Error, "no embedding credential") {
		t.Errorf("status %+v", st)
	}
	// Keyword search keeps working.
	hits, err := c.Search(context.Background(), "summary plan description", 2)
	if err != nil || len(hits) == 0 || hits[0].Section.ID != "planning/rule-of-55/#the-plan-document" {
		t.Errorf("hits %v %v", hits, err)
	}
	// A broken resolver is reported and retried later, never fatal.
	c3, _ := New(Options{Site: testSite(t), SiteURL: "https://host/docs", Dir: t.TempDir(),
		Embedding: func(context.Context) (ai.Credential, error) { return ai.Credential{}, errors.New("vault down") }})
	defer c3.Close()
	if err := c3.Sync(context.Background()); err == nil || !strings.Contains(c3.Status().Vectors.Error, "vault down") {
		t.Errorf("err %v status %+v", err, c3.Status().Vectors)
	}
}
