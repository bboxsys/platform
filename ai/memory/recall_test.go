package memory

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"github.com/bboxsys/platform/ai"
	"github.com/bboxsys/platform/ai/rag"
	"github.com/philippgille/chromem-go"
)

// hashEmbedder is a deterministic bag-of-words embedding (see the rag
// package tests): identical texts get identical vectors and texts that
// share words are close.
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

func newTestRecall(t *testing.T) *Recall {
	t.Helper()
	ix, err := rag.Open(t.TempDir())
	if err != nil {
		t.Fatalf("rag.Open: %v", err)
	}
	t.Cleanup(func() { ix.Close() })
	return NewRecall(ix, hashEmbedder(64))
}

func TestRecallOrderingAndRecency(t *testing.T) {
	ctx := context.Background()
	r := newTestRecall(t)
	const acct, user = "acct1", "U1"

	// Nothing remembered yet: an empty collection is not an error.
	got, err := r.Recall(ctx, acct, user, "anything", 5)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty recall: %v %v", got, err)
	}
	if pre, err := r.Preamble(ctx, acct, user, "anything", 5); err != nil || pre != "" {
		t.Fatalf("empty preamble: %q %v", pre, err)
	}

	base := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	// The same fact remembered 90 days ago and today: the recent one wins
	// although the similarity is identical.
	r.now = func() time.Time { return base.Add(-90 * 24 * time.Hour) }
	old, err := r.Remember(ctx, acct, user, "The deploy window is Tuesday morning", map[string]string{"topic": "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	r.now = func() time.Time { return base }
	recent, err := r.Remember(ctx, acct, user, "The deploy window is Tuesday morning", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Remember(ctx, acct, user, "Backups run nightly to the vault bucket", nil); err != nil {
		t.Fatal(err)
	}
	if old.CreatedAt.Equal(recent.CreatedAt) || old.ID == recent.ID {
		t.Fatalf("hook did not change created_at: %v %v", old.CreatedAt, recent.CreatedAt)
	}

	// Three half-lives cut the old copy to 0.125 of its similarity, which
	// drops it below the weakly related but fresh backups fact.
	got, err = r.Recall(ctx, acct, user, "when is the deploy window", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d facts: %v", len(got), got)
	}
	if got[0].ID != recent.ID || !strings.Contains(got[1].Text, "Backups") || got[2].ID != old.ID {
		t.Errorf("order: got %s, %s, %s; want recent %s, backups, old %s", got[0].ID, got[1].ID, got[2].ID, recent.ID, old.ID)
	}
	if got[0].Similarity != got[2].Similarity {
		t.Errorf("identical texts differ in similarity: %v %v", got[0].Similarity, got[2].Similarity)
	}
	if ratio := got[2].Score / got[0].Score; math.Abs(ratio-0.125) > 0.01 {
		t.Errorf("recency ratio %v, want 0.125", ratio)
	}
	if got[2].Tags["topic"] != "deploy" || got[0].Tags != nil {
		t.Errorf("tags: %v %v", got[2].Tags, got[0].Tags)
	}
	if !got[2].CreatedAt.Equal(base.Add(-90 * 24 * time.Hour)) {
		t.Errorf("created_at round trip: %v", got[2].CreatedAt)
	}

	// n truncates after scoring, so the best-scored fact is the one kept.
	got, err = r.Recall(ctx, acct, user, "when is the deploy window", 1)
	if err != nil || len(got) != 1 || got[0].ID != recent.ID {
		t.Errorf("n=1: %v %v", got, err)
	}

	// The floor keeps a ten-year-old fact reachable: it comes back with
	// score similarity * 0.1 rather than decaying to nothing.
	r.now = func() time.Time { return base.Add(-10 * 365 * 24 * time.Hour) }
	ancient, err := r.Remember(ctx, acct, user, "Weekly report emails go to the ops alias", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.now = func() time.Time { return base }
	got, err = r.Recall(ctx, acct, user, "Weekly report emails go to the ops alias", 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, g := range got {
		if g.ID != ancient.ID {
			continue
		}
		found = true
		if g.Similarity < 0.99 || math.Abs(g.Score-float64(g.Similarity)*MinRecency) > 1e-6 {
			t.Errorf("ancient fact: similarity %v score %v, want score = similarity * %v", g.Similarity, g.Score, MinRecency)
		}
	}
	if !found {
		t.Errorf("ancient fact not recalled: %v", got)
	}

	// The floor keeps very old facts reachable.
	if f := recencyFactor(base, base.Add(-10*365*24*time.Hour)); f != MinRecency {
		t.Errorf("floor: %v", f)
	}
	if f := recencyFactor(base, base.Add(time.Hour)); f != 1 {
		t.Errorf("future created_at: %v", f)
	}
	if f := recencyFactor(base, base.Add(-HalfLife)); math.Abs(f-0.5) > 1e-9 {
		t.Errorf("half-life: %v", f)
	}

	// Preamble lists the facts, newest first for ties.
	pre, err := r.Preamble(ctx, acct, user, "deploy window", 2)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(pre), "\n")
	if lines[0] != PreambleHeader || len(lines) != 3 {
		t.Fatalf("preamble: %q", pre)
	}
	if !strings.HasPrefix(lines[1], "- [2026-10-17] The deploy window") || !strings.HasPrefix(lines[2], "- [2026-07-19] The deploy window") {
		t.Errorf("preamble lines: %q", lines[1:])
	}

	// Validation.
	if _, err := r.Remember(ctx, acct, user, "   ", nil); err == nil {
		t.Errorf("empty fact accepted")
	}
	if _, err := r.Remember(ctx, acct, user, "x", map[string]string{"": "v"}); err == nil {
		t.Errorf("empty tag key accepted")
	}
	if _, err := r.Recall(ctx, acct, user, "", 5); err == nil {
		t.Errorf("empty query accepted")
	}
	if _, err := r.Recall(ctx, acct, user, "x", 0); err == nil {
		t.Errorf("n=0 accepted")
	}
	if _, err := r.Recall(ctx, "", user, "x", 1); err == nil {
		t.Errorf("empty account accepted")
	}
}

func TestRecallTombstoneAndIsolation(t *testing.T) {
	ctx := context.Background()
	r := newTestRecall(t)
	const acct = "acct1"

	f1, err := r.Remember(ctx, acct, "U1", "User one prefers dark mode", nil)
	if err != nil {
		t.Fatal(err)
	}
	f2, err := r.Remember(ctx, acct, "U1", "User one prefers large fonts", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Remember(ctx, acct, "U2", "User two prefers dark mode", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Remember(ctx, "acct2", "U1", "Other account prefers dark mode", nil); err != nil {
		t.Fatal(err)
	}

	// A user sees only their own facts in their account.
	got, err := r.Recall(ctx, acct, "U1", "prefers dark mode", 10)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, g := range got {
		ids[g.ID] = true
		if g.UserID != "U1" {
			t.Errorf("leaked fact from %s: %s", g.UserID, g.Text)
		}
	}
	if len(got) != 2 || !ids[f1.ID] || !ids[f2.ID] {
		t.Errorf("U1 recall: %v", got)
	}
	got, err = r.Recall(ctx, acct, "U3", "prefers dark mode", 10)
	if err != nil || len(got) != 0 {
		t.Errorf("unknown user recall: %v %v", got, err)
	}

	// Forget tombstones; the fact disappears from recall but the id stays
	// in the collection as a tombstone.
	if err := r.Forget(ctx, acct, "U1", f1.ID); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	got, err = r.Recall(ctx, acct, "U1", "prefers dark mode", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != f2.ID {
		t.Errorf("after Forget: %v", got)
	}
	col, err := r.index.Collection(acct, RecallPurpose, r.embed)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := col.GetByID(ctx, f1.ID)
	if err != nil || doc.Metadata[MetaDeleted] != "true" || len(doc.Embedding) == 0 {
		t.Errorf("tombstone: %v %v", doc.Metadata, err)
	}
	// Forgetting again is a no-op; another user's or a missing id is not found.
	if err := r.Forget(ctx, acct, "U1", f1.ID); err != nil {
		t.Errorf("second Forget: %v", err)
	}
	if err := r.Forget(ctx, acct, "U2", f2.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-user Forget: %v", err)
	}
	if err := r.Forget(ctx, acct, "U1", "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing Forget: %v", err)
	}
	// U2 still has its fact.
	if got, _ := r.Recall(ctx, acct, "U2", "dark mode", 5); len(got) != 1 {
		t.Errorf("U2 after U1 forget: %v", got)
	}
}

func TestRecallTools(t *testing.T) {
	ctx := context.Background()
	r := newTestRecall(t)
	remember := RememberTool(r)
	recall := RecallTool(r)
	if remember.Name() != "remember" || remember.ReadOnly() {
		t.Errorf("remember shape: %s %v", remember.Name(), remember.ReadOnly())
	}
	if recall.Name() != "recall" || !recall.ReadOnly() {
		t.Errorf("recall shape: %s %v", recall.Name(), recall.ReadOnly())
	}
	for _, tool := range []ai.Tool{remember, recall} {
		var schema map[string]any
		if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
			t.Errorf("%s schema: %v", tool.Name(), err)
		}
		res, err := tool.Execute(ctx, json.RawMessage(`{"text":"x","query":"x"}`))
		if err != nil || !res.IsError || !strings.Contains(res.Content, "caller") {
			t.Errorf("%s without caller: %+v %v", tool.Name(), res, err)
		}
	}

	cctx := ai.WithCaller(ctx, ai.Caller{AccountID: "acct1", UserID: "U1"})
	res, err := remember.Execute(cctx, json.RawMessage(`{"text":"The staging cluster is named orion","tags":{"env":"staging"}}`))
	if err != nil || res.IsError || !strings.HasPrefix(res.Content, "Remembered fact ") {
		t.Fatalf("remember: %+v %v", res, err)
	}
	var fact Fact
	if err := json.Unmarshal(res.JSON, &fact); err != nil || fact.ID == "" || fact.Tags["env"] != "staging" {
		t.Errorf("remember JSON: %s %v", res.JSON, err)
	}
	res, err = remember.Execute(cctx, json.RawMessage(`{"text":"  "}`))
	if err != nil || !res.IsError {
		t.Errorf("remember empty text: %+v %v", res, err)
	}

	res, err = recall.Execute(cctx, json.RawMessage(`{"query":"what is the staging cluster called"}`))
	if err != nil || res.IsError {
		t.Fatalf("recall: %+v %v", res, err)
	}
	if !strings.HasPrefix(res.Content, "1. id="+fact.ID) || !strings.Contains(res.Content, "env=staging") || !strings.Contains(res.Content, "named orion") {
		t.Errorf("recall output: %q", res.Content)
	}
	other := ai.WithCaller(ctx, ai.Caller{AccountID: "acct2", UserID: "U1"})
	res, err = recall.Execute(other, json.RawMessage(`{"query":"staging cluster"}`))
	if err != nil || res.IsError || res.Content != "No facts recalled." {
		t.Errorf("other account recall: %+v %v", res, err)
	}
	res, err = recall.Execute(cctx, json.RawMessage(`{"query":""}`))
	if err != nil || !res.IsError {
		t.Errorf("recall empty query: %+v %v", res, err)
	}
}

// countingEmbedder wraps embed and counts its calls.
func countingEmbedder(embed chromem.EmbeddingFunc, calls *atomic.Int32) chromem.EmbeddingFunc {
	return func(ctx context.Context, text string) ([]float32, error) {
		calls.Add(1)
		return embed(ctx, text)
	}
}

// Regression: the account's memory collection is shared, but each
// Recall embeds with its own credential's function, never the first
// caller's.
func TestRecallUsesCallerEmbedder(t *testing.T) {
	ctx := context.Background()
	ix, err := rag.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ix.Close() })
	var aCalls, bCalls atomic.Int32
	a := NewRecall(ix, countingEmbedder(hashEmbedder(64), &aCalls))
	negB := func(ctx context.Context, text string) ([]float32, error) {
		v, err := hashEmbedder(64)(ctx, text)
		for i := range v {
			v[i] = -v[i]
		}
		return v, err
	}
	b := NewRecall(ix, countingEmbedder(negB, &bCalls))

	if _, err := a.Remember(ctx, "acct1", "UA", "Caller A prefers vim", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Recall(ctx, "acct1", "UA", "editor", 1); err != nil {
		t.Fatal(err)
	}
	before := aCalls.Load()
	if before == 0 {
		t.Fatal("caller A's embedder was not used")
	}

	fb, err := b.Remember(ctx, "acct1", "UB", "Caller B prefers emacs", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.Recall(ctx, "acct1", "UB", "Caller B prefers emacs", 1)
	if err != nil || len(got) != 1 || got[0].ID != fb.ID {
		t.Fatalf("B recall: %v %v", got, err)
	}
	if got[0].Similarity < 0.99 {
		t.Errorf("B's fact was not embedded with B's function: similarity %v", got[0].Similarity)
	}
	if _, err := b.Preamble(ctx, "acct1", "UB", "emacs", 1); err != nil {
		t.Fatal(err)
	}
	if err := b.Forget(ctx, "acct1", "UB", fb.ID); err != nil {
		t.Fatal(err)
	}
	if aCalls.Load() != before {
		t.Errorf("caller B's operations called caller A's embedder %d times", aCalls.Load()-before)
	}
	if bCalls.Load() < 3 {
		t.Errorf("caller B's embedder called %d times, want at least 3", bCalls.Load())
	}
}

// A changed embedding model leaves old facts unrecalled instead of
// failing the turn.
func TestRecallEmbeddingDimensionChange(t *testing.T) {
	ctx := context.Background()
	ix, err := rag.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ix.Close() })
	old := NewRecall(ix, hashEmbedder(64))
	if _, err := old.Remember(ctx, "acct1", "U1", "The staging cluster is orion", nil); err != nil {
		t.Fatal(err)
	}
	cur := NewRecall(ix, hashEmbedder(32))
	got, err := cur.Recall(ctx, "acct1", "U1", "staging cluster", 5)
	if err != nil || len(got) != 0 {
		t.Errorf("recall across dimensions: %v %v", got, err)
	}
	if pre, err := cur.Preamble(ctx, "acct1", "U1", "staging cluster", 5); err != nil || pre != "" {
		t.Errorf("preamble across dimensions: %q %v", pre, err)
	}
	f, err := cur.Remember(ctx, "acct1", "U1", "The staging cluster is lyra", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err = cur.Recall(ctx, "acct1", "U1", "staging cluster", 5)
	if err != nil || len(got) != 1 || got[0].ID != f.ID {
		t.Errorf("recall at current dimension: %v %v", got, err)
	}
}
