package docsite

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// Hit is one keyword search result.
type Hit struct {
	Section *Section
	// Score is the BM25 score; only hits above zero are returned.
	Score float64
	// Snippet is a window of the section text around the first query term
	// found, or the start of the text.
	Snippet string
}

// SnippetRunes is the length of a Hit's snippet.
const SnippetRunes = 320

// Search ranks sections by keyword relevance (BM25 over the section text,
// with the heading path counted twice) and returns at most n hits, best
// first. Commands, configuration keys, and paths are indexed both whole
// ("ai.allow_writes", "api/v1/queue") and by their parts, so an exact
// identifier finds its section and so does a word from it. It needs no
// embedding credential.
func (s *Site) Search(query string, n int) []Hit {
	if s == nil || s.index == nil {
		return nil
	}
	terms := tokenize(query)
	if len(terms) == 0 || n <= 0 {
		return nil
	}
	scored := s.index.score(terms)
	if len(scored) > n {
		scored = scored[:n]
	}
	hits := make([]Hit, 0, len(scored))
	for _, sc := range scored {
		sec := s.index.docs[sc.doc]
		hits = append(hits, Hit{Section: sec, Score: sc.score, Snippet: snippet(sec.Text, terms)})
	}
	return hits
}

// keywordIndex is an in-memory BM25 index over sections.
type keywordIndex struct {
	docs    []*Section
	tf      []map[string]int
	lengths []int
	df      map[string]int
	avgLen  float64
}

const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

func newKeywordIndex(docs []*Section) *keywordIndex {
	ix := &keywordIndex{docs: docs, df: map[string]int{}}
	total := 0
	for _, d := range docs {
		tf := map[string]int{}
		n := 0
		// Headings count twice: a query naming a section should find it
		// ahead of a section that merely mentions the words.
		for i := 0; i < 2; i++ {
			for _, t := range tokenize(d.HeadingPath()) {
				tf[t]++
				n++
			}
		}
		for _, t := range tokenize(d.Text) {
			tf[t]++
			n++
		}
		for t := range tf {
			ix.df[t]++
		}
		ix.tf = append(ix.tf, tf)
		ix.lengths = append(ix.lengths, n)
		total += n
	}
	if len(docs) > 0 {
		ix.avgLen = float64(total) / float64(len(docs))
	}
	return ix
}

type scoredDoc struct {
	doc   int
	score float64
}

func (ix *keywordIndex) score(terms []string) []scoredDoc {
	N := float64(len(ix.docs))
	scores := map[int]float64{}
	seen := map[string]bool{}
	for _, t := range terms {
		if seen[t] {
			continue
		}
		seen[t] = true
		df, ok := ix.df[t]
		if !ok {
			continue
		}
		idf := math.Log(1 + (N-float64(df)+0.5)/(float64(df)+0.5))
		for i, tf := range ix.tf {
			f, ok := tf[t]
			if !ok {
				continue
			}
			ff := float64(f)
			norm := bm25K1 * (1 - bm25B + bm25B*float64(ix.lengths[i])/ix.avgLen)
			scores[i] += idf * (ff * (bm25K1 + 1)) / (ff + norm)
		}
	}
	out := make([]scoredDoc, 0, len(scores))
	for i, sc := range scores {
		if sc > 0 {
			out = append(out, scoredDoc{doc: i, score: sc})
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].score != out[b].score {
			return out[a].score > out[b].score
		}
		return out[a].doc < out[b].doc
	})
	return out
}

// tokenize lowercases text and splits it into terms: each word's
// alphanumeric parts (lightly stemmed), plus the word itself when it is a
// compound identifier such as a dotted configuration key or a path.
func tokenize(s string) []string {
	var out []string
	for _, f := range strings.Fields(strings.ToLower(s)) {
		f = strings.TrimFunc(f, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsNumber(r)
		})
		if f == "" {
			continue
		}
		parts := strings.FieldsFunc(f, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsNumber(r)
		})
		if len(parts) > 1 {
			out = append(out, f)
		}
		for _, p := range parts {
			out = append(out, stem(p))
		}
	}
	return out
}

// stem folds simple plurals so "portfolios" matches "portfolio". It is
// applied to documents and queries alike, so it only has to be consistent.
func stem(w string) string {
	switch {
	case len(w) > 4 && strings.HasSuffix(w, "ies"):
		return w[:len(w)-3] + "y"
	case len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us"):
		return w[:len(w)-1]
	}
	return w
}

// snippet returns a window of text around the first query term found,
// trying longer terms first so a compound identifier wins over its parts.
func snippet(text string, terms []string) string {
	lower := strings.ToLower(text)
	sorted := append([]string(nil), terms...)
	sort.SliceStable(sorted, func(a, b int) bool { return len(sorted[a]) > len(sorted[b]) })
	at := -1
	for _, t := range sorted {
		// Short terms are mostly stopwords; a window around "the" says
		// nothing about why the section matched.
		if len(t) < 4 {
			continue
		}
		if i := strings.Index(lower, t); i >= 0 {
			at = i
			break
		}
	}
	if at <= 0 {
		return trimRunes(text, SnippetRunes)
	}
	r := []rune(text)
	pos := len([]rune(text[:at]))
	start := pos - SnippetRunes/3
	if start < 0 {
		start = 0
	}
	for start > 0 && !unicode.IsSpace(r[start]) {
		start--
	}
	window := strings.TrimSpace(string(r[start:]))
	if start > 0 {
		return "... " + trimRunes(window, SnippetRunes)
	}
	return trimRunes(window, SnippetRunes)
}

// Excerpt returns a window of text around the first term of query found
// in it, or its start: the snippet Search attaches to a hit, for callers
// ranking sections another way.
func Excerpt(text, query string) string {
	return snippet(text, tokenize(query))
}
