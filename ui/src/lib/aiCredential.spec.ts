import { describe, it, expect } from 'vitest'
import type { APIToken } from '../types/admin'
import {
  aiCredentialLabel, claudeCredentialForm, isClaudeCredential, isOtherModelCredential, ollamaCredentialForm, AI_MODEL_PROVIDERS, OTHER_MODEL_PROVIDERS,
  AI_PROVIDER_DEFAULT_ENDPOINTS, DEFAULT_CLAUDE_MODEL, CLAUDE_MODELS, emptyAICredentialForm, readAICredential, validateAICredential, withAICredential,
} from './aiCredential'

function token(overrides: Partial<APIToken> = {}): APIToken {
  return {
    name: 't', type: 'bearer', scope: 'user', id: '', has_token: true, metadata: {},
    user_id: 'u', created_at: '', updated_at: '', ...overrides,
  }
}

describe('readAICredential', () => {
  it('treats a token without an AI resource type as general and keeps its endpoint', () => {
    const form = readAICredential(token({ metadata: { resource_type: 'cloudflare', endpoint: 'https://api.example', tags: { ai_model: 'stale' } } }))
    expect(form).toEqual({ use: 'general', provider: '', model: '', effort: '', endpoint: 'https://api.example', trustReadOnly: false })
    expect(readAICredential(token()).use).toBe('general')
  })

  it('reads the provider, model, effort, and endpoint of a model credential', () => {
    const form = readAICredential(token({
      metadata: {
        resource_type: 'ai_model', endpoint: 'https://gateway.example',
        tags: { ai_provider: 'anthropic', ai_model: 'claude-opus-5-5', ai_effort: 'high', ai_max_output: '8000' },
      },
    }))
    expect(form).toEqual({ use: 'ai_model', provider: 'anthropic', model: 'claude-opus-5-5', effort: 'high', endpoint: 'https://gateway.example', trustReadOnly: false })
  })

  it('reads an embedding and an MCP server credential', () => {
    expect(readAICredential(token({ metadata: { resource_type: 'ai_embedding', tags: { ai_provider: 'voyage', ai_model: 'voyage-3' } } })))
      .toMatchObject({ use: 'ai_embedding', provider: 'voyage', model: 'voyage-3', effort: '' })
    expect(readAICredential(token({ metadata: { resource_type: 'mcp_server', endpoint: 'https://mcp.example/mcp' } })))
      .toMatchObject({ use: 'mcp_server', endpoint: 'https://mcp.example/mcp' })
  })
})

describe('withAICredential', () => {
  const base = { name: 'n', type: 'bearer' as const, metadata: { tags: { custom: 'x', oidc_auth_mode: 'static' } } }

  it('writes the resource type, endpoint, and tags of a model credential over the caller metadata', () => {
    const out = withAICredential(base, emptyAICredentialForm({
      use: 'ai_model', provider: 'anthropic', model: ' claude-opus-5-5 ', effort: 'max', endpoint: ' https://gateway.example ',
    }))
    expect(out.metadata).toEqual({
      resource_type: 'ai_model', endpoint: 'https://gateway.example',
      tags: { custom: 'x', oidc_auth_mode: 'static', ai_provider: 'anthropic', ai_model: 'claude-opus-5-5', ai_effort: 'max' },
    })
    expect(out.name).toBe('n')
  })

  it('leaves the endpoint undefined when blank and omits the effort for an embedding', () => {
    const out = withAICredential(base, emptyAICredentialForm({ use: 'ai_embedding', provider: 'voyage', model: 'voyage-3', effort: 'high' }))
    expect(out.metadata?.resource_type).toBe('ai_embedding')
    expect(out.metadata?.endpoint).toBeUndefined()
    expect(out.metadata?.tags).toEqual({ custom: 'x', oidc_auth_mode: 'static', ai_provider: 'voyage', ai_model: 'voyage-3' })
  })

  it('writes only the endpoint for an MCP server', () => {
    const out = withAICredential(base, emptyAICredentialForm({ use: 'mcp_server', provider: 'anthropic', model: 'm', endpoint: 'https://mcp.example/mcp' }))
    expect(out.metadata).toEqual({ resource_type: 'mcp_server', endpoint: 'https://mcp.example/mcp', tags: { custom: 'x', oidc_auth_mode: 'static' } })
  })

  it('leaves a general token alone apart from removing the AI tags', () => {
    const stale = { ...base, metadata: { resource_type: 'cloudflare', endpoint: 'https://api.example', tags: { custom: 'x', ai_provider: 'anthropic', ai_model: 'old', ai_effort: 'low' } } }
    const out = withAICredential(stale, emptyAICredentialForm({ use: 'general', provider: 'anthropic', model: 'ignored' }))
    expect(out.metadata).toEqual({ resource_type: 'cloudflare', endpoint: 'https://api.example', tags: { custom: 'x' } })

    const bare = withAICredential({ name: 'n', type: 'bearer' as const, metadata: { tags: { ai_model: 'old' } } }, emptyAICredentialForm())
    expect(bare.metadata).toEqual({})
    expect(withAICredential({ name: 'n', type: 'bearer' as const }, emptyAICredentialForm()).metadata).toBeUndefined()
  })
})

describe('validateAICredential', () => {
  it('accepts any general token', () => {
    expect(validateAICredential(emptyAICredentialForm())).toBeNull()
  })

  it('requires a supported provider for a model', () => {
    const form = emptyAICredentialForm({ use: 'ai_model' })
    expect(validateAICredential(form)).toMatch(/provider is required/)
    expect(validateAICredential({ ...form, provider: 'anthropic' })).toBeNull()
    expect(validateAICredential({ ...form, provider: 'voyage' })).toMatch(/not supported for chat models/)
    expect(validateAICredential({ ...form, provider: 'ollama' })).toBeNull()
    expect(validateAICredential({ ...form, provider: 'openai_compat' })).toMatch(/needs an endpoint/)
    expect(validateAICredential({ ...form, provider: 'openai_compat', endpoint: 'https://llm.example/v1' })).toBeNull()
    expect(validateAICredential({ ...form, provider: 'anthropic', endpoint: 'gateway.example' })).toMatch(/http or https URL/)
  })

  it('applies the same rules to an embedding credential', () => {
    const form = emptyAICredentialForm({ use: 'ai_embedding', provider: 'ollama', model: 'nomic-embed-text' })
    expect(validateAICredential(form)).toBeNull()
    expect(validateAICredential({ ...form, provider: '' })).toMatch(/provider is required/)
    expect(validateAICredential({ ...form, provider: 'openai_compat' })).toMatch(/needs an endpoint/)
  })

  it('requires an endpoint for an MCP server', () => {
    const form = emptyAICredentialForm({ use: 'mcp_server' })
    expect(validateAICredential(form)).toMatch(/MCP server needs an endpoint/)
    expect(validateAICredential({ ...form, endpoint: 'mcp.example' })).toMatch(/http or https URL/)
    expect(validateAICredential({ ...form, endpoint: 'http://mcp.example/mcp' })).toBeNull()
  })
})

describe('aiCredentialLabel', () => {
  it('names the model, the embedding model, or the MCP endpoint', () => {
    expect(aiCredentialLabel(token({ metadata: { resource_type: 'ai_model', tags: { ai_provider: 'anthropic', ai_model: 'claude-opus-5-5' } } })))
      .toBe('AI model: claude-opus-5-5')
    expect(aiCredentialLabel(token({ metadata: { resource_type: 'ai_model', tags: { ai_provider: 'anthropic' } } })))
      .toBe('AI model: Anthropic')
    expect(aiCredentialLabel(token({ metadata: { resource_type: 'ai_embedding', tags: { ai_provider: 'voyage', ai_model: 'voyage-3' } } })))
      .toBe('AI embedding: voyage-3')
    expect(aiCredentialLabel(token({ metadata: { resource_type: 'mcp_server', endpoint: 'https://mcp.example/mcp' } })))
      .toBe('MCP server: https://mcp.example/mcp')
    expect(aiCredentialLabel(token({ metadata: { resource_type: 'mcp_server' } }))).toBe('MCP server')
  })

  it('returns null for a general token', () => {
    expect(aiCredentialLabel(token({ metadata: { resource_type: 'cloudflare', resource_name: 'zone' } }))).toBeNull()
    expect(aiCredentialLabel(token())).toBeNull()
  })
})

describe('MCP server trust', () => {
  it('round-trips the read-only trust tag only for an MCP server', () => {
    const token = { name: 'n', type: 'bearer', scope: 'account', id: '', has_token: true, user_id: 'U', created_at: '', updated_at: '',
      metadata: { resource_type: 'mcp_server', endpoint: 'https://mcp.example/mcp', tags: { mcp_trust_read_only: 'true' } } } as unknown as APIToken
    expect(readAICredential(token).trustReadOnly).toBe(true)
    const base = { name: 'n', type: 'bearer' as const }
    const on = withAICredential(base, emptyAICredentialForm({ use: 'mcp_server', endpoint: 'https://mcp.example/mcp', trustReadOnly: true }))
    expect(on.metadata?.tags?.mcp_trust_read_only).toBe('true')
    const off = withAICredential(on, emptyAICredentialForm({ use: 'mcp_server', endpoint: 'https://mcp.example/mcp' }))
    expect(off.metadata?.tags?.mcp_trust_read_only).toBeUndefined()
    const model = withAICredential(base, emptyAICredentialForm({ use: 'ai_model', provider: 'anthropic', trustReadOnly: true }))
    expect(model.metadata?.tags?.mcp_trust_read_only).toBeUndefined()
  })
})

describe('Claude presets', () => {
  it('prefills a Claude form with the public API and the default model', () => {
    const form = claudeCredentialForm()
    expect(form).toMatchObject({ use: 'ai_model', provider: 'anthropic', model: DEFAULT_CLAUDE_MODEL, endpoint: 'https://api.anthropic.com' })
    expect(validateAICredential(form)).toBeNull()
    expect(CLAUDE_MODELS[0].id).toBe(DEFAULT_CLAUDE_MODEL)
    expect(AI_PROVIDER_DEFAULT_ENDPOINTS.openai_compat).toBeUndefined()
  })

  it('prefills an Ollama form with the local server root and no model', () => {
    const form = ollamaCredentialForm()
    expect(form).toMatchObject({ use: 'ai_model', provider: 'ollama', model: '', endpoint: 'http://localhost:11434' })
    expect(validateAICredential(form)).toBeNull()
    expect(OTHER_MODEL_PROVIDERS).toEqual(['ollama', 'openai_compat'])
    expect(AI_MODEL_PROVIDERS).toContain('anthropic')
  })

  it('recognizes a Claude credential by resource type and provider', () => {
    const tok = (resource_type: string, tags?: Record<string, string>) =>
      ({ name: 'n', type: 'bearer', scope: 'user', id: '', has_token: true, user_id: 'U', created_at: '', updated_at: '',
        metadata: { resource_type, tags } }) as unknown as APIToken
    expect(isClaudeCredential(tok('ai_model', { ai_provider: 'anthropic' }))).toBe(true)
    expect(isClaudeCredential(tok('ai_model'))).toBe(true)
    expect(isClaudeCredential(tok('ai_model', { ai_provider: 'openai_compat' }))).toBe(false)
    expect(isClaudeCredential(tok('ai_embedding', { ai_provider: 'anthropic' }))).toBe(false)
    expect(isOtherModelCredential(tok('ai_model', { ai_provider: 'ollama' }))).toBe(true)
    expect(isOtherModelCredential(tok('ai_model', { ai_provider: 'openai_compat' }))).toBe(true)
    expect(isOtherModelCredential(tok('ai_model'))).toBe(false)
    expect(isOtherModelCredential(tok('ai_embedding', { ai_provider: 'ollama' }))).toBe(false)
  })
})
