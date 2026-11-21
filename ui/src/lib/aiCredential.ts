import type { APIToken, APITokenInput, APITokenMetadata } from '../types/admin'

// The metadata of an AI credential in the admin vault, as the platform's
// ai package reads it. This is the wire contract with ai/credential.go in
// Go (bboxsys/platform): a model, embedding, or MCP server credential is an
// ordinary API token whose resource_type says what it opens, whose endpoint
// is the base URL, and whose tags carry the provider and model. Keep the
// two in step.

export const AI_RESOURCE_TYPE_MODEL = 'ai_model'
export const AI_RESOURCE_TYPE_EMBEDDING = 'ai_embedding'
export const AI_RESOURCE_TYPE_MCP_SERVER = 'mcp_server'

export const AI_PROVIDER_TAG = 'ai_provider'
export const AI_MODEL_TAG = 'ai_model'
export const AI_CONTEXT_WINDOW_TAG = 'ai_context_window'
export const AI_MAX_OUTPUT_TAG = 'ai_max_output'
export const AI_EMBEDDING_DIMENSIONS_TAG = 'ai_embedding_dimensions'
export const AI_EFFORT_TAG = 'ai_effort'
// MCP_TRUST_READ_ONLY_TAG = 'true' lets the assistant believe a remote MCP
// server's readOnlyHint; otherwise every remote tool is treated as a write.
export const MCP_TRUST_READ_ONLY_TAG = 'mcp_trust_read_only'

// The tags an AI credential owns; a token that stops being one loses them.
export const AI_TAGS = [
  AI_PROVIDER_TAG, AI_MODEL_TAG, AI_CONTEXT_WINDOW_TAG, AI_MAX_OUTPUT_TAG, AI_EMBEDDING_DIMENSIONS_TAG, AI_EFFORT_TAG,
  MCP_TRUST_READ_ONLY_TAG,
] as const

export const AI_PROVIDER_ANTHROPIC = 'anthropic'
export const AI_PROVIDER_OPENAI_COMPAT = 'openai_compat'
export const AI_PROVIDER_VOYAGE = 'voyage'
export const AI_PROVIDER_OLLAMA = 'ollama'

export type AIProvider = 'anthropic' | 'openai_compat' | 'voyage' | 'ollama'

export const AI_PROVIDER_LABELS: Record<AIProvider, string> = {
  anthropic:     'Anthropic',
  openai_compat: 'OpenAI-compatible',
  voyage:        'Voyage',
  ollama:        'Ollama',
}

// The providers that serve chat models, and the ones that serve embeddings.
// Only providers the Go side has a factory for are listed for models:
// anthropic (ai/anthropic), and ollama and openai_compat (ai/openai, the
// OpenAI chat completions dialect that Ollama, llama-server, vLLM, and LM
// Studio all serve).
export const AI_MODEL_PROVIDERS: AIProvider[] = ['anthropic', 'ollama', 'openai_compat']
export const AI_EMBEDDING_PROVIDERS: AIProvider[] = ['voyage', 'openai_compat', 'ollama']

// The model providers other than Claude, which has its own form.
export const OTHER_MODEL_PROVIDERS: AIProvider[] = AI_MODEL_PROVIDERS.filter(p => p !== 'anthropic')

// The base URL each provider's public API documents. The Go side uses the
// same values when a credential leaves the endpoint empty (the Anthropic SDK
// default, VoyageEndpoint in ai/rag, and ai.OllamaDefaultEndpoint), so
// prefilling the form with them changes nothing at runtime; it only shows
// the user what will be called. The Ollama endpoint is the server root: the
// Go side appends /v1 for chat and /api for embeddings, and accepts either
// suffix if the user typed one. An OpenAI-compatible endpoint has no default.
export const AI_PROVIDER_DEFAULT_ENDPOINTS: Partial<Record<AIProvider, string>> = {
  anthropic: 'https://api.anthropic.com',
  voyage:    'https://api.voyageai.com/v1',
  ollama:    'http://localhost:11434',
}

// The current Claude models, most capable first. DEFAULT_CLAUDE_MODEL is
// what ai/anthropic uses when a credential names no model (DefaultModel).
export const DEFAULT_CLAUDE_MODEL = 'claude-opus-5-5'
export interface ClaudeModel { id: string; label: string }
export const CLAUDE_MODELS: ClaudeModel[] = [
  { id: 'claude-opus-5-5',   label: 'Claude Opus 5.5 (default)' },
  { id: 'claude-fable-5-1',  label: 'Claude Fable 5.1' },
  { id: 'claude-sonnet-5-5', label: 'Claude Sonnet 5.5' },
  { id: 'claude-haiku-5-5',  label: 'Claude Haiku 5.5' },
]

// A form prefilled for a new Claude credential: Anthropic, the default
// model, and the public API base URL.
export function claudeCredentialForm(): AICredentialForm {
  return emptyAICredentialForm({
    use: 'ai_model',
    provider: AI_PROVIDER_ANTHROPIC,
    model: DEFAULT_CLAUDE_MODEL,
    endpoint: AI_PROVIDER_DEFAULT_ENDPOINTS.anthropic ?? '',
  })
}

// Reports whether a token is a Claude model credential: an AI model whose
// provider is Anthropic (or unset, which the server treats as Anthropic).
export function isClaudeCredential(token: APIToken): boolean {
  const m = token.metadata ?? {}
  if (m.resource_type !== AI_RESOURCE_TYPE_MODEL) return false
  const provider = m.tags?.[AI_PROVIDER_TAG] ?? ''
  return provider === '' || provider === AI_PROVIDER_ANTHROPIC
}

// A form prefilled for a new Ollama model credential: the local server
// root and no model, since the server's own list (shown by Test) is the
// source of model names. The same form serves an OpenAI-compatible server
// once the provider is switched.
export function ollamaCredentialForm(): AICredentialForm {
  return emptyAICredentialForm({
    use: 'ai_model',
    provider: AI_PROVIDER_OLLAMA,
    endpoint: AI_PROVIDER_DEFAULT_ENDPOINTS.ollama ?? '',
  })
}

// Reports whether a token is a model credential for a provider other than
// Claude (Ollama or an OpenAI-compatible server).
export function isOtherModelCredential(token: APIToken): boolean {
  const m = token.metadata ?? {}
  return m.resource_type === AI_RESOURCE_TYPE_MODEL && !isClaudeCredential(token)
}

export type AIEffort = 'low' | 'medium' | 'high' | 'xhigh' | 'max'
export const AI_EFFORTS: AIEffort[] = ['low', 'medium', 'high', 'xhigh', 'max']

// What a token is for. 'general' is every token that is not an AI
// credential; the others are the AI resource types.
export type AICredentialUse = 'general' | 'ai_model' | 'ai_embedding' | 'mcp_server'

export const AI_CREDENTIAL_USES: AICredentialUse[] = ['general', 'ai_model', 'ai_embedding', 'mcp_server']

export const AI_CREDENTIAL_USE_LABELS: Record<AICredentialUse, string> = {
  general:      'General',
  ai_model:     'AI model',
  ai_embedding: 'AI embedding',
  mcp_server:   'MCP server',
}

// The state an AI credential form binds to. Fields that do not apply to the
// use are kept but ignored on write.
export interface AICredentialForm {
  use: AICredentialUse
  provider: string
  model: string
  effort: string
  endpoint: string
  // trustReadOnly applies to an MCP server only (MCP_TRUST_READ_ONLY_TAG).
  trustReadOnly: boolean
}

export function emptyAICredentialForm(overrides: Partial<AICredentialForm> = {}): AICredentialForm {
  return { use: 'general', provider: '', model: '', effort: '', endpoint: '', trustReadOnly: false, ...overrides }
}

function useOf(resourceType: string | undefined): AICredentialUse {
  switch (resourceType) {
    case AI_RESOURCE_TYPE_MODEL:
    case AI_RESOURCE_TYPE_EMBEDDING:
    case AI_RESOURCE_TYPE_MCP_SERVER:
      return resourceType
    default:
      return 'general'
  }
}

// Reads the AI settings off a token the server returned. A token whose
// resource_type is not an AI kind is 'general' with the other fields blank
// except the endpoint, which every token may carry.
export function readAICredential(token: APIToken): AICredentialForm {
  const m = token.metadata ?? {}
  const tags = m.tags ?? {}
  const use = useOf(m.resource_type)
  if (use === 'general') return emptyAICredentialForm({ endpoint: m.endpoint ?? '' })
  return {
    use,
    provider: tags[AI_PROVIDER_TAG] ?? '',
    model:    tags[AI_MODEL_TAG] ?? '',
    effort:   tags[AI_EFFORT_TAG] ?? '',
    endpoint: m.endpoint ?? '',
    trustReadOnly: tags[MCP_TRUST_READ_ONLY_TAG] === 'true',
  }
}

// Applies the form to a write body: sets metadata.resource_type and
// endpoint and writes the AI tags. 'general' leaves resource_type and
// endpoint to the caller and removes the AI tags, so a token that stops
// being a credential does not keep a stale provider. Other tags are kept.
export function withAICredential<T extends Partial<APITokenInput>>(
  body: T, form: AICredentialForm,
): T & Partial<APITokenInput> {
  const tags: Record<string, string> = { ...body.metadata?.tags }
  for (const t of AI_TAGS) delete tags[t]
  if (form.use === 'general') {
    if (!body.metadata) return { ...body }
    const metadata: APITokenMetadata = { ...body.metadata }
    if (Object.keys(tags).length > 0) metadata.tags = tags
    else delete metadata.tags
    return { ...body, metadata }
  }
  const endpoint = form.endpoint.trim()
  if (form.use !== AI_RESOURCE_TYPE_MCP_SERVER) {
    if (form.provider.trim()) tags[AI_PROVIDER_TAG] = form.provider.trim()
    if (form.model.trim()) tags[AI_MODEL_TAG] = form.model.trim()
  }
  if (form.use === AI_RESOURCE_TYPE_MODEL && form.effort) tags[AI_EFFORT_TAG] = form.effort
  if (form.use === AI_RESOURCE_TYPE_MCP_SERVER && form.trustReadOnly) tags[MCP_TRUST_READ_ONLY_TAG] = 'true'
  return {
    ...body,
    metadata: {
      ...body.metadata,
      resource_type: form.use,
      endpoint: endpoint || undefined,
      tags,
    },
  }
}

// Validates a form before it is sent. Returns the message to show, or null.
export function validateAICredential(form: AICredentialForm): string | null {
  const endpoint = form.endpoint.trim()
  switch (form.use) {
    case AI_RESOURCE_TYPE_MODEL:
    case AI_RESOURCE_TYPE_EMBEDDING:
      if (!form.provider.trim()) return 'A provider is required.'
      if (form.use === AI_RESOURCE_TYPE_MODEL && !AI_MODEL_PROVIDERS.includes(form.provider as AIProvider)) {
        return `${AI_PROVIDER_LABELS[form.provider as AIProvider] ?? form.provider} is not supported for chat models yet.`
      }
      if (form.provider === AI_PROVIDER_OPENAI_COMPAT && !endpoint) return 'An OpenAI-compatible provider needs an endpoint.'
      if (endpoint && !/^https?:\/\/\S+$/.test(endpoint)) return 'The endpoint must be an http or https URL.'
      return null
    case AI_RESOURCE_TYPE_MCP_SERVER:
      if (!endpoint) return 'An MCP server needs an endpoint.'
      if (!/^https?:\/\/\S+$/.test(endpoint)) return 'The endpoint must be an http or https URL.'
      return null
    default:
      return null
  }
}

// A short label for a token list: "AI model: claude-opus-5-5", "AI embedding:
// voyage-3", "MCP server: https://mcp.example". Null for a general token.
export function aiCredentialLabel(token: APIToken): string | null {
  const form = readAICredential(token)
  if (form.use === 'general') return null
  const what = form.use === AI_RESOURCE_TYPE_MCP_SERVER
    ? form.endpoint
    : form.model || (form.provider && AI_PROVIDER_LABELS[form.provider as AIProvider]) || form.provider
  const label = AI_CREDENTIAL_USE_LABELS[form.use]
  return what ? `${label}: ${what}` : label
}
