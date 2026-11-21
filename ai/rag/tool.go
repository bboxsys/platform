package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/bboxsys/platform/ai"
	"github.com/philippgille/chromem-go"
)

// SearchToolName is the name of the tool SearchTool returns.
const SearchToolName = "search_documents"

const searchSchema = `{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "What to look for, as a question or a few keywords."},
    "n": {"type": "integer", "minimum": 1, "maximum": 20, "default": 5, "description": "How many results to return."}
  },
  "required": ["query"]
}`

// SearchTool returns the read-only "search_documents" tool. It queries the
// caller's account collection for purpose (ai.CallerFromContext supplies
// the account; a missing caller is an error result) and returns a numbered
// list of hits with id, similarity, metadata, and content.
func SearchTool(index *Index, embed chromem.EmbeddingFunc, purpose string) ai.Tool {
	desc := fmt.Sprintf("Search the %s indexed for this account by meaning and return the most relevant passages with their ids and metadata. Use it before answering questions about the account's own material.", purpose)
	return ai.NewTool(SearchToolName, desc, json.RawMessage(searchSchema), true,
		func(ctx context.Context, input json.RawMessage) (ai.Result, error) {
			caller, ok := ai.CallerFromContext(ctx)
			if !ok || caller.AccountID == "" {
				return ai.ErrorResult("search_documents: no caller identity in context"), nil
			}
			var in struct {
				Query string `json:"query"`
				N     int    `json:"n"`
			}
			if len(input) > 0 {
				if err := json.Unmarshal(input, &in); err != nil {
					return ai.ErrorResult("search_documents: invalid input: %v", err), nil
				}
			}
			in.Query = strings.TrimSpace(in.Query)
			if in.Query == "" {
				return ai.ErrorResult("search_documents: query is required"), nil
			}
			if in.N <= 0 {
				in.N = 5
			}
			if in.N > 20 {
				in.N = 20
			}
			col, err := index.Collection(caller.AccountID, purpose, embed)
			if err != nil {
				return ai.Result{}, err
			}
			hits, err := Query(ctx, col, in.Query, in.N, nil)
			if err != nil {
				return ai.Result{}, err
			}
			return ai.Result{Content: FormatResults(hits)}, nil
		})
}

// FormatResults renders hits as the numbered list the search tool returns.
func FormatResults(hits []Result) string {
	if len(hits) == 0 {
		return "No matching documents."
	}
	var b strings.Builder
	for i, h := range hits {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "%d. id=%s similarity=%.3f", i+1, h.ID, h.Similarity)
		keys := make([]string, 0, len(h.Metadata))
		for k := range h.Metadata {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, " %s=%s", k, h.Metadata[k])
		}
		b.WriteString("\n")
		b.WriteString(h.Content)
	}
	return b.String()
}
