package rag

import (
	"context"
	"errors"
	"hash/fnv"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/bboxsys/platform/ai"
	"github.com/philippgille/chromem-go"
)

// hashEmbedder is a deterministic bag-of-words embedding: every word is
// hashed into one of dims buckets with a sign, and the vector is
// L2-normalized. It needs no network and makes texts that share words
// similar.
func hashEmbedder(dims int) chromem.EmbeddingFunc {
	return func(_ context.Context, text string) ([]float32, error) {
		v := make([]float32, dims)
		words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
		for _, w := range words {
			h := fnv.New32a()
			h.Write([]byte(w))
			sum := h.Sum32()
			sign := float32(1)
			if (sum>>8)&1 == 1 {
				sign = -1
			}
			v[sum%uint32(dims)] += sign
		}
		var norm float64
		for _, x := range v {
			norm += float64(x * x)
		}
		if norm == 0 {
			v[0] = 1
			return v, nil
		}
		n := float32(math.Sqrt(norm))
		for i := range v {
			v[i] /= n
		}
		return v, nil
	}
}

func openTestIndex(t *testing.T) *Index {
	t.Helper()
	ix, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { ix.Close() })
	return ix
}

func TestChunkEdgeCases(t *testing.T) {
	if got := Chunk("", 100, 10); got != nil {
		t.Errorf("empty text: got %q", got)
	}
	if got := Chunk("  \n\n \t", 100, 10); got != nil {
		t.Errorf("whitespace text: got %q", got)
	}
	if got := Chunk("short text", 100, 10); len(got) != 1 || got[0] != "short text" {
		t.Errorf("short text: got %q", got)
	}
	if got := Chunk("a b c", 0, 0); len(got) != 1 || got[0] != "a b c" {
		t.Errorf("size 0: got %q", got)
	}

	// Paragraphs that each fit stay whole and pack together.
	text := "First paragraph here.\n\nSecond paragraph here.\n\nThird paragraph here."
	got := Chunk(text, 50, 0)
	if len(got) != 2 {
		t.Fatalf("paragraph packing: got %d chunks %q", len(got), got)
	}
	if got[0] != "First paragraph here.\n\nSecond paragraph here." || got[1] != "Third paragraph here." {
		t.Errorf("paragraph packing: got %q", got)
	}

	// A long paragraph splits on sentence ends.
	long := "One sentence. Two sentence! Three sentence? Four sentence."
	got = Chunk(long, 30, 0)
	if len(got) != 2 || got[0] != "One sentence. Two sentence!" || got[1] != "Three sentence? Four sentence." {
		t.Errorf("sentence split: got %q", got)
	}

	// Overlap carries the trailing sentence into the next chunk.
	got = Chunk(long, 30, 15)
	if len(got) < 2 || !strings.HasPrefix(got[1], "Two sentence!") {
		t.Errorf("overlap: got %q", got)
	}

	// A single run of text without sentence ends is hard-split at spaces,
	// no chunk exceeds the size, nothing is empty, and nothing is lost.
	words := strings.Repeat("word ", 100)
	got = Chunk(words, 40, 0)
	if len(got) < 10 {
		t.Errorf("hard split: got %d chunks", len(got))
	}
	for _, c := range got {
		if c == "" {
			t.Errorf("hard split: empty chunk")
		}
		if utf8.RuneCountInString(c) > 40 {
			t.Errorf("hard split: chunk %q longer than 40", c)
		}
	}
	if strings.Count(strings.Join(got, " "), "word") != 100 {
		t.Errorf("hard split: lost words in %q", got)
	}

	// Unbroken runes (no spaces) are cut at size.
	got = Chunk(strings.Repeat("x", 25), 10, 0)
	if len(got) != 3 || got[0] != "xxxxxxxxxx" || got[2] != "xxxxx" {
		t.Errorf("rune split: got %q", got)
	}

	// Multi-byte runes are counted as runes, not bytes.
	got = Chunk(strings.Repeat("é", 25), 10, 0)
	if len(got) != 3 || utf8.RuneCountInString(got[0]) != 10 {
		t.Errorf("multibyte split: got %q", got)
	}

	// Overlap at least size is clamped rather than looping forever.
	got = Chunk(long, 30, 30)
	if len(got) < 2 {
		t.Errorf("clamped overlap: got %q", got)
	}
}

func TestAddQueryDeleteRoundTrip(t *testing.T) {
	ctx := context.Background()
	ix := openTestIndex(t)
	embed := hashEmbedder(64)
	col, err := ix.Collection("acct1", "documents", embed)
	if err != nil {
		t.Fatalf("Collection: %v", err)
	}

	// Empty collection queries return nothing rather than chromem's
	// nResults error.
	hits, err := Query(ctx, col, "anything", 5, nil)
	if err != nil || len(hits) != 0 {
		t.Fatalf("empty query: %v %v", hits, err)
	}

	docs := []Document{
		{ID: "cat", Content: "The cat sat on the mat and purred.", Metadata: map[string]string{"kind": "animal"}},
		{ID: "stocks", Content: "Stock prices fell sharply after the earnings report.", Metadata: map[string]string{"kind": "finance"}},
		{ID: "long", Content: strings.Repeat("The quick brown fox jumps over the lazy dog. ", 80), Metadata: map[string]string{"kind": "story"}},
	}
	if err := Add(ctx, col, docs); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if col.Count() < 5 {
		t.Fatalf("expected the long document to be chunked, count=%d", col.Count())
	}
	if _, err := col.GetByID(ctx, "long#0"); err != nil {
		t.Errorf("chunk long#0 missing: %v", err)
	}
	if _, err := col.GetByID(ctx, "cat"); err != nil {
		t.Errorf("short document keeps its id: %v", err)
	}

	// n larger than the collection is clamped.
	hits, err = Query(ctx, col, "cat on the mat", 100, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(hits) != col.Count() {
		t.Errorf("clamped n: got %d hits, want %d", len(hits), col.Count())
	}
	if hits[0].ID != "cat" {
		t.Errorf("best hit: got %s, want cat", hits[0].ID)
	}
	if hits[0].Metadata["kind"] != "animal" || hits[0].Metadata[MetaSourceID] != "cat" || hits[0].Metadata[MetaChunk] != "0" {
		t.Errorf("metadata: %v", hits[0].Metadata)
	}

	// A where filter restricts results.
	hits, err = Query(ctx, col, "cat on the mat", 5, map[string]string{"kind": "finance"})
	if err != nil {
		t.Fatalf("filtered Query: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "stocks" {
		t.Errorf("filtered: got %v", hits)
	}

	// Re-adding a shorter version drops the stale chunks.
	if err := Add(ctx, col, []Document{{ID: "long", Content: "Now short."}}); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if _, err := col.GetByID(ctx, "long#0"); err == nil {
		t.Errorf("stale chunk long#0 survived re-add")
	}
	if _, err := col.GetByID(ctx, "long"); err != nil {
		t.Errorf("re-added document missing: %v", err)
	}

	// Delete removes the source; deleting again is a no-op.
	if err := Delete(ctx, col, "cat"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := col.GetByID(ctx, "cat"); err == nil {
		t.Errorf("cat survived Delete")
	}
	if err := Delete(ctx, col, "cat"); err != nil {
		t.Errorf("second Delete: %v", err)
	}

	// Empty input and invalid documents.
	if err := Add(ctx, col, nil); err != nil {
		t.Errorf("Add nil: %v", err)
	}
	if err := Add(ctx, col, []Document{{ID: "", Content: "x"}}); err == nil {
		t.Errorf("Add without id succeeded")
	}
	if _, err := Query(ctx, col, "", 5, nil); err == nil {
		t.Errorf("Query with empty text succeeded")
	}
}

func TestPersistenceAcrossOpen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	embed := hashEmbedder(64)
	ix, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	col, err := ix.Collection("acct1", "documents", embed)
	if err != nil {
		t.Fatal(err)
	}
	if err := Add(ctx, col, []Document{{ID: "d", Content: "persist me please"}}); err != nil {
		t.Fatal(err)
	}
	ix.Close()
	if _, err := ix.Collection("acct1", "documents", embed); err == nil {
		t.Errorf("Collection after Close succeeded")
	}

	ix2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ix2.Close()
	col2, err := ix2.Collection("acct1", "documents", embed)
	if err != nil {
		t.Fatal(err)
	}
	hits, err := Query(ctx, col2, "persist", 1, nil)
	if err != nil || len(hits) != 1 || hits[0].ID != "d" {
		t.Errorf("reloaded query: %v %v", hits, err)
	}
}

func TestPerAccountIsolation(t *testing.T) {
	ctx := context.Background()
	ix := openTestIndex(t)
	embed := hashEmbedder(64)
	colA, err := ix.Collection("acctA", "documents", embed)
	if err != nil {
		t.Fatal(err)
	}
	colB, err := ix.Collection("acctB", "documents", embed)
	if err != nil {
		t.Fatal(err)
	}
	if colA == colB || colA.Name == colB.Name {
		t.Fatalf("accounts share a collection")
	}
	if err := Add(ctx, colA, []Document{{ID: "secret", Content: "account A secret runbook"}}); err != nil {
		t.Fatal(err)
	}
	hits, err := Query(ctx, colB, "secret runbook", 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("account B saw account A's document: %v", hits)
	}
	// The same purpose and account returns the same collection.
	again, err := ix.Collection("acctA", "documents", embed)
	if err != nil {
		t.Fatal(err)
	}
	if again.Count() != 1 {
		t.Errorf("collection lookup lost documents: count=%d", again.Count())
	}

	for _, bad := range [][2]string{{"", "documents"}, {"acct", ""}, {"a:b", "documents"}, {"acct", "doc:s"}} {
		if _, err := ix.Collection(bad[0], bad[1], embed); err == nil {
			t.Errorf("Collection(%q, %q) succeeded", bad[0], bad[1])
		}
	}
	if _, err := ix.Collection("acct", "documents", nil); err == nil {
		t.Errorf("Collection with nil embedder succeeded")
	}
}

func TestSearchTool(t *testing.T) {
	ctx := context.Background()
	ix := openTestIndex(t)
	embed := hashEmbedder(64)
	tool := SearchTool(ix, embed, "documents")
	if tool.Name() != "search_documents" || !tool.ReadOnly() {
		t.Fatalf("tool shape: name=%s readonly=%v", tool.Name(), tool.ReadOnly())
	}
	if !ai.ToolNamePattern.MatchString(tool.Name()) {
		t.Errorf("tool name does not match pattern")
	}

	// No caller in context is an error result, not a panic or Go error.
	res, err := tool.Execute(ctx, []byte(`{"query":"x"}`))
	if err != nil || !res.IsError || !strings.Contains(res.Content, "caller") {
		t.Fatalf("missing caller: %+v %v", res, err)
	}

	col, err := ix.Collection("acct1", "documents", embed)
	if err != nil {
		t.Fatal(err)
	}
	if err := Add(ctx, col, []Document{
		{ID: "nats", Content: "NATS leaf nodes connect to the hub over TLS.", Metadata: map[string]string{"title": "NATS"}},
		{ID: "badger", Content: "Badger stores values in a log-structured tree.", Metadata: map[string]string{"title": "Badger"}},
	}); err != nil {
		t.Fatal(err)
	}
	cctx := ai.WithCaller(ctx, ai.Caller{AccountID: "acct1", UserID: "U1"})
	res, err = tool.Execute(cctx, []byte(`{"query":"leaf nodes TLS","n":1}`))
	if err != nil || res.IsError {
		t.Fatalf("Execute: %+v %v", res, err)
	}
	if !strings.HasPrefix(res.Content, "1. id=nats similarity=") || !strings.Contains(res.Content, "title=NATS") || !strings.Contains(res.Content, "leaf nodes") {
		t.Errorf("tool output: %q", res.Content)
	}
	if strings.Contains(res.Content, "2. ") {
		t.Errorf("n=1 returned more than one result: %q", res.Content)
	}

	// Another account sees nothing.
	other := ai.WithCaller(ctx, ai.Caller{AccountID: "acct2", UserID: "U2"})
	res, err = tool.Execute(other, []byte(`{"query":"leaf nodes TLS"}`))
	if err != nil || res.IsError || res.Content != "No matching documents." {
		t.Errorf("other account: %+v %v", res, err)
	}

	res, err = tool.Execute(cctx, []byte(`{"query":"  "}`))
	if err != nil || !res.IsError {
		t.Errorf("blank query: %+v %v", res, err)
	}
	res, err = tool.Execute(cctx, []byte(`{bad json`))
	if err != nil || !res.IsError {
		t.Errorf("bad json: %+v %v", res, err)
	}
}

func TestEmbedder(t *testing.T) {
	cases := []struct {
		name string
		cred ai.Credential
		ok   bool
	}{
		{"voyage", ai.Credential{Provider: ai.ProviderVoyage, Bearer: "k"}, true},
		{"voyage no key", ai.Credential{Provider: ai.ProviderVoyage}, false},
		{"openai compat", ai.Credential{Provider: ai.ProviderOpenAICompat, Endpoint: "http://x/v1", Model: "m"}, true},
		{"openai compat no endpoint", ai.Credential{Provider: ai.ProviderOpenAICompat, Model: "m"}, false},
		{"openai compat no model", ai.Credential{Provider: ai.ProviderOpenAICompat, Endpoint: "http://x/v1"}, false},
		{"ollama", ai.Credential{Provider: ai.ProviderOllama}, true},
		{"anthropic", ai.Credential{Provider: ai.ProviderAnthropic, Bearer: "k"}, false},
		{"unknown", ai.Credential{Provider: "cohere"}, false},
		{"empty", ai.Credential{}, false},
		{"wrong type", ai.Credential{Provider: ai.ProviderOllama, ResourceType: ai.ResourceTypeModel}, false},
		{"right type", ai.Credential{Provider: ai.ProviderOllama, ResourceType: ai.ResourceTypeEmbedding}, true},
	}
	for _, c := range cases {
		fn, err := Embedder(c.cred)
		if c.ok && (err != nil || fn == nil) {
			t.Errorf("%s: got err %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: expected error", c.name)
		}
	}
}

// recordingEmbedder wraps embed and counts its calls.
type recordingEmbedder struct {
	mu    sync.Mutex
	calls int
	embed chromem.EmbeddingFunc
}

func (r *recordingEmbedder) fn(ctx context.Context, text string) ([]float32, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return r.embed(ctx, text)
}

func (r *recordingEmbedder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// negated returns embed with every component's sign flipped: same
// dimension, different vectors.
func negated(embed chromem.EmbeddingFunc) chromem.EmbeddingFunc {
	return func(ctx context.Context, text string) ([]float32, error) {
		v, err := embed(ctx, text)
		for i := range v {
			v[i] = -v[i]
		}
		return v, err
	}
}

// Regression: the shared account collection must not keep the first
// caller's embedding function. Every operation of caller B embeds with
// B's function only.
func TestCollectionUsesCallerEmbedder(t *testing.T) {
	ctx := context.Background()
	ix := openTestIndex(t)
	a := &recordingEmbedder{embed: hashEmbedder(64)}
	b := &recordingEmbedder{embed: negated(hashEmbedder(64))}

	colA, err := ix.Collection("acct1", "documents", a.fn)
	if err != nil {
		t.Fatal(err)
	}
	if err := Add(ctx, colA, []Document{{ID: "a", Content: "alpha runbook for the gateway"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Query(ctx, colA, "gateway", 1, nil); err != nil {
		t.Fatal(err)
	}
	if a.count() == 0 {
		t.Fatal("caller A's embedder was not used")
	}
	aCalls := a.count()

	colB, err := ix.Collection("acct1", "documents", b.fn)
	if err != nil {
		t.Fatal(err)
	}
	if err := Add(ctx, colB, []Document{{ID: "b", Content: "beta runbook for the broker"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Query(ctx, colB, "broker", 2, nil); err != nil {
		t.Fatal(err)
	}
	if err := Delete(ctx, colB, "b"); err != nil {
		t.Fatal(err)
	}
	cctx := ai.WithCaller(ctx, ai.Caller{AccountID: "acct1", UserID: "UB"})
	res, err := SearchTool(ix, b.fn, "documents").Execute(cctx, []byte(`{"query":"runbook"}`))
	if err != nil || res.IsError {
		t.Fatalf("search tool: %+v %v", res, err)
	}
	if a.count() != aCalls {
		t.Errorf("caller B's operations called caller A's embedder %d times", a.count()-aCalls)
	}
	if b.count() < 3 {
		t.Errorf("caller B's embedder called %d times, want at least 3", b.count())
	}

	// The shared chromem collection holds only the failing placeholder,
	// so an implicit embed fails loudly.
	err = colB.AddDocument(ctx, chromem.Document{ID: "implicit", Content: "no embedding"})
	if !errors.Is(err, ErrNoEmbedder) {
		t.Errorf("implicit AddDocument: got %v, want ErrNoEmbedder", err)
	}
	if _, err := colB.Collection.Query(ctx, "runbook", 1, nil, nil); !errors.Is(err, ErrNoEmbedder) {
		t.Errorf("implicit Query: got %v, want ErrNoEmbedder", err)
	}
	if a.count() != aCalls {
		t.Errorf("implicit embeds reached caller A's embedder")
	}
}

func TestAddEmbedFailureKeepsOldDocument(t *testing.T) {
	ctx := context.Background()
	ix := openTestIndex(t)
	good, err := ix.Collection("acct1", "documents", hashEmbedder(64))
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 80)
	if err := Add(ctx, good, []Document{{ID: "long", Content: long}}); err != nil {
		t.Fatal(err)
	}
	before := good.Count()

	// Fail on the last chunk only, so earlier chunks embed successfully
	// before the error.
	var calls atomic.Int32
	failing := func(ctx context.Context, text string) ([]float32, error) {
		if calls.Add(1) >= 3 {
			return nil, errors.New("embedding service down")
		}
		return hashEmbedder(64)(ctx, text)
	}
	bad, err := ix.Collection("acct1", "documents", failing)
	if err != nil {
		t.Fatal(err)
	}
	err = Add(ctx, bad, []Document{{ID: "long", Content: strings.Repeat("A replacement sentence about otters. ", 120)}})
	if err == nil || !strings.Contains(err.Error(), "embedding service down") {
		t.Fatalf("Add with failing embedder: %v", err)
	}
	if good.Count() != before {
		t.Errorf("count after failed re-index: %d, want %d", good.Count(), before)
	}
	doc, err := good.GetByID(ctx, "long#0")
	if err != nil || !strings.Contains(doc.Content, "quick brown fox") {
		t.Errorf("old chunk lost after failed re-index: %v", err)
	}
	hits, err := Query(ctx, good, "quick brown fox", 1, nil)
	if err != nil || len(hits) != 1 || hits[0].Metadata[MetaSourceID] != "long" {
		t.Errorf("old document not searchable: %v %v", hits, err)
	}
}

func TestQueryEmbeddingDimension(t *testing.T) {
	ctx := context.Background()
	ix := openTestIndex(t)
	old, err := ix.Collection("acct1", "documents", hashEmbedder(64))
	if err != nil {
		t.Fatal(err)
	}
	if err := Add(ctx, old, []Document{{ID: "old", Content: "legacy gateway runbook"}}); err != nil {
		t.Fatal(err)
	}
	doc, err := old.GetByID(ctx, "old")
	if err != nil || doc.Metadata[MetaEmbedDim] != "64" {
		t.Fatalf("embed_dim: %v %v", doc.Metadata, err)
	}

	// The account switched to a 32-dimension model: the old vectors are
	// skipped, not an error.
	cur, err := ix.Collection("acct1", "documents", hashEmbedder(32))
	if err != nil {
		t.Fatal(err)
	}
	hits, err := Query(ctx, cur, "gateway runbook", 5, nil)
	if err != nil || len(hits) != 0 {
		t.Fatalf("mismatched dimension: %v %v", hits, err)
	}
	if err := Add(ctx, cur, []Document{{ID: "new", Content: "current gateway runbook", Metadata: map[string]string{MetaEmbedDim: "999"}}}); err != nil {
		t.Fatal(err)
	}
	hits, err = Query(ctx, cur, "gateway runbook", 5, nil)
	if err != nil || len(hits) != 1 || hits[0].ID != "new" || hits[0].Metadata[MetaEmbedDim] != "32" {
		t.Errorf("current dimension: %v %v", hits, err)
	}
	// A caller where filter combines with the dimension filter.
	hits, err = Query(ctx, old, "gateway runbook", 5, map[string]string{MetaSourceID: "old"})
	if err != nil || len(hits) != 1 || hits[0].ID != "old" {
		t.Errorf("old dimension with filter: %v %v", hits, err)
	}
}
