import { describe, it, expect } from 'vitest'
import type { APIToken } from '../types/admin'
import {
  apiTokenAuthForm, apiTokenAuthSecret, apiTokenAuthTags, emptyAPITokenAuthForm, readAPITokenAuth,
  validateAPITokenAuth, withAPITokenAuth,
} from './apitokenAuth'

function token(overrides: Partial<APIToken> = {}): APIToken {
  return {
    name: 't', type: 'bearer', scope: 'user', id: '', has_token: true, metadata: {},
    user_id: 'u', created_at: '', updated_at: '', ...overrides,
  }
}

describe('readAPITokenAuth', () => {
  it('treats a token without a mode tag as static and ignores its identity', () => {
    const auth = readAPITokenAuth(token({ id: 'me@example.com' }))
    expect(auth.authMode).toBe('static')
    expect(auth.oidcClientId).toBe('')
    expect(auth.oidcTokenField).toBe('access_token')
    expect(auth.oidcClientAuthMethod).toBe('auto')
  })

  it('reads the OIDC settings from the tags and the client ID from id', () => {
    const auth = readAPITokenAuth(token({
      id: 'client',
      metadata: { tags: {
        oidc_auth_mode: 'password', oidc_issuer: 'https://idp/realm', oidc_username: 'bot',
        oidc_scopes: 'openid', oidc_token_field: 'id_token', oidc_client_auth_method: 'client_secret_post',
        oidc_insecure_skip_verify: 'true',
      } },
    }))
    expect(auth).toEqual({
      authMode: 'password', oidcIssuer: 'https://idp/realm', oidcClientId: 'client', oidcUsername: 'bot',
      oidcScopes: 'openid', oidcTokenField: 'id_token', oidcClientAuthMethod: 'client_secret_post',
      oidcInsecureSkipVerify: true,
    })
    expect(apiTokenAuthForm(auth)).toMatchObject({ ...auth, token: '', oidcClientSecret: '', oidcPassword: '', oidcRefreshToken: '' })
  })
})

describe('apiTokenAuthTags / apiTokenAuthSecret', () => {
  it('records only the mode for a static token and sends the bearer as the secret', () => {
    const form = emptyAPITokenAuthForm({ token: 'bearer' })
    expect(apiTokenAuthTags(form)).toEqual({ oidc_auth_mode: 'static' })
    expect(apiTokenAuthSecret(form)).toBe('bearer')
    expect(apiTokenAuthSecret(emptyAPITokenAuthForm())).toBeUndefined()
  })

  it('records every OIDC setting, normalized, and packs the secrets as one document', () => {
    const form = emptyAPITokenAuthForm({
      authMode: 'refresh_token', oidcIssuer: ' https://idp/realm/ ', oidcClientId: 'c', oidcScopes: ' openid ',
      oidcRefreshToken: 'r', oidcClientSecret: 's', oidcInsecureSkipVerify: true,
    })
    expect(apiTokenAuthTags(form)).toEqual({
      oidc_auth_mode: 'refresh_token', oidc_issuer: 'https://idp/realm', oidc_scopes: 'openid', oidc_username: '',
      oidc_token_field: 'access_token', oidc_client_auth_method: 'auto', oidc_insecure_skip_verify: 'true',
    })
    expect(JSON.parse(apiTokenAuthSecret(form)!)).toEqual({ version: 1, client_secret: 's', refresh_token: 'r' })
  })

  it('keeps the stored secret when no OIDC secret was entered', () => {
    const form = emptyAPITokenAuthForm({ authMode: 'client_credentials', oidcIssuer: 'https://idp', oidcClientId: 'c' })
    expect(apiTokenAuthSecret(form)).toBeUndefined()
  })
})

describe('withAPITokenAuth', () => {
  it('sets the client ID, merges tags over the caller metadata, and sets the secret only when entered', () => {
    const base = { name: 'n', type: 'bearer' as const, metadata: { resource_type: 'example', tags: { custom: 'x' } } }
    const withSecret = withAPITokenAuth(base, emptyAPITokenAuthForm({
      authMode: 'client_credentials', oidcIssuer: 'https://idp', oidcClientId: ' c ', oidcClientSecret: 's',
    }))
    expect(withSecret.id).toBe('c')
    expect(withSecret.metadata?.resource_type).toBe('example')
    expect(withSecret.metadata?.tags).toMatchObject({ custom: 'x', oidc_auth_mode: 'client_credentials', oidc_issuer: 'https://idp' })
    expect(withSecret.token).toBeDefined()

    const kept = withAPITokenAuth({ ...base, token: 'stale' }, emptyAPITokenAuthForm({ authMode: 'client_credentials', oidcIssuer: 'https://idp', oidcClientId: 'c' }))
    expect(kept.token).toBeUndefined()

    const stat = withAPITokenAuth({ ...base, id: 'me' }, emptyAPITokenAuthForm({ token: 'b' }))
    expect(stat.id).toBe('me')
    expect(stat.token).toBe('b')
  })
})

describe('validateAPITokenAuth', () => {
  const stored = readAPITokenAuth(token({
    id: 'c', metadata: { tags: { oidc_auth_mode: 'client_credentials', oidc_issuer: 'https://idp' } },
  }))

  it('requires the secrets of a new credential', () => {
    expect(validateAPITokenAuth(emptyAPITokenAuthForm(), null)).toMatch(/bearer token is required/)
    expect(validateAPITokenAuth(emptyAPITokenAuthForm({ token: 'b' }), null)).toBeNull()
    const cc = emptyAPITokenAuthForm({ authMode: 'client_credentials', oidcIssuer: 'https://idp', oidcClientId: 'c' })
    expect(validateAPITokenAuth(cc, null)).toMatch(/client secret/)
    expect(validateAPITokenAuth({ ...cc, oidcClientSecret: 's' }, null)).toBeNull()
    expect(validateAPITokenAuth({ ...cc, oidcIssuer: 'http://idp' }, null)).toMatch(/https/)
    expect(validateAPITokenAuth({ ...cc, oidcClientId: '' }, null)).toMatch(/issuer and client ID/)
    const pw = emptyAPITokenAuthForm({ authMode: 'password', oidcIssuer: 'https://idp', oidcClientId: 'c', oidcClientSecret: 's' })
    expect(validateAPITokenAuth(pw, null)).toMatch(/username and password/)
    expect(validateAPITokenAuth({ ...pw, oidcUsername: 'u', oidcPassword: 'p' }, null)).toBeNull()
    const rt = emptyAPITokenAuthForm({ authMode: 'refresh_token', oidcIssuer: 'https://idp', oidcClientId: 'c', oidcClientAuthMethod: 'none' })
    expect(validateAPITokenAuth(rt, null)).toMatch(/refresh token/)
    expect(validateAPITokenAuth({ ...rt, oidcRefreshToken: 'r' }, null)).toBeNull()
  })

  it('lets an edit keep the stored secrets unless the mode or identity changes', () => {
    const unchanged = apiTokenAuthForm(stored)
    expect(validateAPITokenAuth(unchanged, stored)).toBeNull()
    expect(validateAPITokenAuth({ ...unchanged, oidcIssuer: 'https://idp/' }, stored)).toBeNull()
    expect(validateAPITokenAuth({ ...unchanged, oidcClientId: 'other' }, stored)).toMatch(/client secret/)
    expect(validateAPITokenAuth({ ...unchanged, authMode: 'password', oidcUsername: 'u' }, stored)).toMatch(/username and password/)
    expect(validateAPITokenAuth({ ...unchanged, authMode: 'static' }, stored)).toMatch(/bearer token/)
  })

  it('requires the whole secret set when any secret is re-entered', () => {
    const rt = apiTokenAuthForm({ ...stored, authMode: 'refresh_token' })
    expect(validateAPITokenAuth({ ...rt, oidcRefreshToken: 'r' }, { ...stored, authMode: 'refresh_token' })).toMatch(/Re-enter the client secret/)
    expect(validateAPITokenAuth({ ...rt, oidcRefreshToken: 'r', oidcClientAuthMethod: 'none' }, { ...stored, authMode: 'refresh_token' })).toBeNull()
    expect(validateAPITokenAuth({ ...rt, oidcRefreshToken: 'r', oidcClientSecret: 's' }, { ...stored, authMode: 'refresh_token' })).toBeNull()
  })
})
