// Package ai is the platform's agent loop: a model provider, a set of
// tools, a session of append-only messages, and the loop that runs a
// prompt to completion while streaming events to the caller.
//
// The package is provider-neutral and product-free. A model provider
// (package ai/anthropic) turns one request into one assistant turn; tools
// are anything that satisfies Tool (package ai/apitool generates them from
// an OpenAPI document, ai/rag and ai/memory contribute retrieval and
// memory, ai/mcp bridges to and from MCP servers); sessions persist in the
// host's store. The host process that holds the admin store satisfies
// CredentialResolver so that the model endpoint and key come from the
// admin API token vault and never reach a browser.
//
// Message history is stored in the provider's native content-block form
// and is append-only: current models bind their reasoning blocks to the
// conversation that produced them, so a harness must never rewrite earlier
// turns. Context pressure is handled by the provider (server-side
// compaction), not by editing history here.
//
// Import boundary: this package and its subpackages import admin/client,
// never the root admin package. The root admin package imports them to
// host the assistant.
package ai
