package instance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/bboxsys/platform/ai"
	"github.com/bboxsys/platform/ai/mcp"
	"github.com/bboxsys/platform/ai/memory"
	"github.com/bboxsys/platform/ai/rag"
	"github.com/philippgille/chromem-go"
)

// Tools builds the caller's tool set: the generated API tools, the
// generic request tool, the host's own tools (Options.Tools) and the
// documentation tools (Options.Docs), the memory notes tool, and, when the caller has an embedding credential, document
// search plus remember and recall over the index. Every remote MCP server credential visible to the caller is
// connected and its tools added; a server that cannot be reached within
// MCPConnectTimeout is logged and skipped. Credentials repeating a name
// already seen are skipped (a vault resolver lists the user's own before
// the account's, so the user's wins), as is any remote tool whose name is
// already taken, so one misconfigured server cannot break the whole set.
// A server's readOnlyHint is trusted only when its credential carries
// ai.TagMCPTrustReadOnly = "true". The returned remote tools implement
// io.Closer; CloseTools releases them.
func (i *Instance) Tools(ctx context.Context, c ai.Caller) ([]ai.Tool, error) {
	tools := make([]ai.Tool, 0, len(i.apiTools)+8)
	tools = append(tools, i.apiTools...)
	if i.reqTool != nil {
		tools = append(tools, i.reqTool)
	}
	var extra []ai.Tool
	if i.opts.Tools != nil {
		hostTools, err := i.opts.Tools(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("ai/instance: host tools: %w", err)
		}
		extra = append(extra, hostTools...)
	}
	for _, corpus := range i.opts.Docs {
		if corpus != nil {
			extra = append(extra, corpus.Tools(i.opts.ToolPrefix)...)
		}
	}
	if len(extra) > 0 {
		names := make(map[string]bool, len(tools))
		for _, t := range tools {
			names[t.Name()] = true
		}
		for _, t := range extra {
			if names[t.Name()] {
				i.log.WarnE().Str("tool", t.Name()).Msg("ai: host tool name already taken; skipped")
				continue
			}
			names[t.Name()] = true
			tools = append(tools, t)
		}
	}
	tools = append(tools, memory.NotesTool(i.notes))
	if embed, ok := i.embedder(ctx, c); ok {
		recall := memory.NewRecall(i.index, embed)
		tools = append(tools,
			rag.SearchTool(i.index, embed, DocumentsPurpose),
			memory.RememberTool(recall),
			memory.RecallTool(recall),
		)
	}
	creds, err := i.opts.Credentials.MCPServers(ctx, c.AccountID, c.UserID)
	if err != nil {
		i.log.ErrorE().Err(err).Msg("ai: listing mcp server credentials failed")
		return tools, nil
	}
	taken := make(map[string]bool, len(tools))
	for _, t := range tools {
		taken[t.Name()] = true
	}
	servers := make(map[string]bool, len(creds))
	for _, cred := range creds {
		if servers[cred.Name] {
			i.log.WarnE().Str("server", cred.Name).Msg("ai: mcp server credential name repeated; skipped")
			continue
		}
		servers[cred.Name] = true
		remote, closer, err := connectMCP(ctx, cred)
		if err != nil {
			i.log.WarnE().Str("server", cred.Name).Err(err).Msg("ai: mcp server unavailable; skipped")
			continue
		}
		added := 0
		for _, t := range remote {
			if taken[t.Name()] {
				i.log.WarnE().Str("server", cred.Name).Str("tool", t.Name()).Msg("ai: mcp tool name already taken; skipped")
				continue
			}
			taken[t.Name()] = true
			tools = append(tools, &closingTool{Tool: t, closer: closer})
			added++
		}
		if added == 0 {
			closer.Close()
		}
	}
	return tools, nil
}

// CloseTools closes every tool that implements io.Closer.
func CloseTools(tools []ai.Tool) {
	for _, t := range tools {
		if c, ok := t.(io.Closer); ok {
			c.Close()
		}
	}
}

// connectMCP connects one remote server and lists its tools, bounded by
// MCPConnectTimeout. The session outlives that bound; closer ends it.
func connectMCP(ctx context.Context, cred ai.Credential) ([]ai.Tool, *sharedCloser, error) {
	cctx, cancel := context.WithTimeout(ctx, MCPConnectTimeout)
	defer cancel()
	client, err := mcp.ConnectWithOptions(cctx, cred.Name, cred.Endpoint, cred.Bearer, mcp.ClientOptions{
		TrustReadOnly: cred.Tag(ai.TagMCPTrustReadOnly) == "true",
	})
	if err != nil {
		return nil, nil, err
	}
	remote, err := client.Tools(cctx)
	if err != nil {
		client.Close()
		return nil, nil, err
	}
	return remote, &sharedCloser{c: client}, nil
}

// embedder returns the embedding function for the caller's embedding
// credential, or false when the caller has none or it cannot be built.
func (i *Instance) embedder(ctx context.Context, c ai.Caller) (chromem.EmbeddingFunc, bool) {
	cred, err := i.opts.Credentials.Embedding(ctx, c.AccountID, c.UserID, "")
	if err != nil {
		if !errors.Is(err, ai.ErrNoCredential) {
			i.log.ErrorE().Err(err).Msg("ai: embedding credential")
		}
		return nil, false
	}
	embed, err := rag.Embedder(cred)
	if err != nil {
		i.log.ErrorE().Str("credential", cred.Name).Err(err).Msg("ai: embedding function")
		return nil, false
	}
	return embed, true
}

// preamble recalls the facts most relevant to the prompt when the caller
// has an embedding credential.
func (i *Instance) preamble(ctx context.Context, c ai.Caller, prompt string) string {
	embed, ok := i.embedder(ctx, c)
	if !ok {
		return ""
	}
	pre, err := memory.NewRecall(i.index, embed).Preamble(ctx, c.AccountID, c.UserID, prompt, 5)
	if err != nil {
		i.log.ErrorE().Err(err).Msg("ai: recall preamble failed")
		return ""
	}
	return pre
}

// sharedCloser closes one remote MCP client once, however many of its
// tools are closed.
type sharedCloser struct {
	once sync.Once
	c    *mcp.Client
}

func (s *sharedCloser) Close() error {
	var err error
	s.once.Do(func() { err = s.c.Close() })
	return err
}

// closingTool is a remote tool that releases its client on Close.
type closingTool struct {
	ai.Tool
	closer *sharedCloser
}

func (t *closingTool) Close() error { return t.closer.Close() }

// pathSegments splits a route into its segments, ignoring a trailing slash.
func pathSegments(route string) []string {
	return strings.Split(strings.Trim(route, "/"), "/")
}

// segmentMatch reports whether a path segment matches a pattern segment:
// "*" and template segments ("{id}") match anything, others must be equal.
func segmentMatch(seg, pattern string) bool {
	return pattern == "*" || strings.HasPrefix(pattern, "{") || seg == pattern
}

// PathHasPrefix reports whether path (a template or a concrete path)
// starts with every segment of prefix, where a "*" or "{name}" segment of
// prefix matches any one segment. It is the building block of a
// ToolFilter deny list.
func PathHasPrefix(path, prefix string) bool {
	segs, pat := pathSegments(path), pathSegments(prefix)
	if len(segs) < len(pat) {
		return false
	}
	for k := range pat {
		if !segmentMatch(segs[k], pat[k]) {
			return false
		}
	}
	return true
}

// PathMatches reports whether path matches the template exactly.
func PathMatches(path, template string) bool {
	return len(pathSegments(path)) == len(pathSegments(template)) && PathHasPrefix(path, template)
}
