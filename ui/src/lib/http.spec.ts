import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { request, withQuery, HttpError, outstanding } from './http'

// Builds a fake Response-like object good enough for request()/fetch mocking.
function fakeResponse(opts: { ok?: boolean; status?: number; text: string }): Response {
  const status = opts.status ?? (opts.ok === false ? 500 : 200)
  return {
    ok: opts.ok ?? status < 400,
    status,
    text: async () => opts.text,
  } as unknown as Response
}

// Reads the Authorization header out of whatever init object request() passed.
function authHeader(init: RequestInit | undefined): string | null {
  const h = init?.headers
  if (h instanceof Headers) return h.get('Authorization')
  return null
}

describe('withQuery', () => {
  it('returns the url unchanged when no query is given', () => {
    expect(withQuery('/api/x')).toBe('/api/x')
  })

  it('keeps empty-string values and drops undefined', () => {
    const out = withQuery('/api/x', { filter: '', page: 2, skip: undefined })
    expect(out).toContain('filter=')
    expect(out).toContain('page=2')
    expect(out).not.toContain('skip')
    // empty string is sent as "filter=" (a real value, e.g. clearing a filter)
    expect(out).toMatch(/[?&]filter=(&|$)/)
  })

  it('uses & when the url already has a query string', () => {
    expect(withQuery('/api/x?a=1', { b: 2 })).toBe('/api/x?a=1&b=2')
  })
})

describe('request', () => {
  beforeEach(() => {
    outstanding.value = 0
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('returns parsed JSON on a 2xx JSON body', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => fakeResponse({ text: JSON.stringify({ hello: 'world' }) })))
    const out = await request<{ hello: string }>('/api/x')
    expect(out).toEqual({ hello: 'world' })
  })

  it('returns undefined for an empty 2xx body', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => fakeResponse({ text: '' })))
    const out = await request<unknown>('/api/x')
    expect(out).toBeUndefined()
  })

  it('throws HttpError with status and body on a non-2xx response', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => fakeResponse({ ok: false, status: 404, text: 'not found' })))
    await expect(request('/api/x')).rejects.toBeInstanceOf(HttpError)
    try {
      await request('/api/x')
    } catch (e) {
      const err = e as HttpError
      expect(err.status).toBe(404)
      expect(err.body).toBe('not found')
    }
  })

  it('throws HttpError (not a SyntaxError) on a 2xx non-JSON body', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => fakeResponse({ status: 200, text: '<html>oops</html>' })))
    let caught: unknown
    try {
      await request('/api/x')
    } catch (e) {
      caught = e
    }
    expect(caught).toBeInstanceOf(HttpError)
    expect(caught).not.toBeInstanceOf(SyntaxError)
    expect((caught as HttpError).status).toBe(200)
    expect((caught as HttpError).body).toBe('<html>oops</html>')
  })

  it('decrements the outstanding counter even when the request fails', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => fakeResponse({ ok: false, status: 500, text: 'boom' })))
    await expect(request('/api/x')).rejects.toBeInstanceOf(HttpError)
    expect(outstanding.value).toBe(0)
  })

  it('sends a Bearer Authorization header when given a token', async () => {
    const fetchMock = vi.fn(async (_url: string | URL, _init?: RequestInit) => fakeResponse({ text: '{}' }))
    vi.stubGlobal('fetch', fetchMock)
    await request('/api/x', { auth: { token: 'abc123', accountId: 'acct1' } })
    const init = fetchMock.mock.calls[0][1] as RequestInit
    expect(authHeader(init)).toBe('Bearer abc123')
    expect((init.headers as Headers).get('X-Blackbox-AccountID')).toBe('acct1')
  })

  it('sends a Basic Authorization header for basic auth', async () => {
    const fetchMock = vi.fn(async (_url: string | URL, _init?: RequestInit) => fakeResponse({ text: '{}' }))
    vi.stubGlobal('fetch', fetchMock)
    await request('/api/x', { auth: { basic: { email: 'a@b.com', password: 'pw' } } })
    const init = fetchMock.mock.calls[0][1] as RequestInit
    const header = authHeader(init)
    expect(header).toMatch(/^Basic /)
    const decoded = atob(header!.slice('Basic '.length))
    expect(decoded).toBe('a@b.com:pw')
  })

  it('utf8-encodes basic-auth credentials with a non-ASCII password without throwing', async () => {
    const fetchMock = vi.fn(async (_url: string | URL, _init?: RequestInit) => fakeResponse({ text: '{}' }))
    vi.stubGlobal('fetch', fetchMock)
    // A naive btoa() on this password would throw an InvalidCharacterError.
    const password = 'pä55wörd-🔒'
    await expect(
      request('/api/x', { auth: { basic: { email: 'user@example.com', password } } }),
    ).resolves.toBeDefined()
    const init = fetchMock.mock.calls[0][1] as RequestInit
    const header = authHeader(init)!
    expect(header).toMatch(/^Basic /)
    const b64 = header.slice('Basic '.length)
    // Valid base64 that round-trips back to the UTF-8 bytes of the credentials.
    const bytes = Uint8Array.from(atob(b64), c => c.charCodeAt(0))
    const decoded = new TextDecoder().decode(bytes)
    expect(decoded).toBe(`user@example.com:${password}`)
  })

  it('sends a JSON body and Content-Type when json is provided', async () => {
    const fetchMock = vi.fn(async (_url: string | URL, _init?: RequestInit) => fakeResponse({ text: '{}' }))
    vi.stubGlobal('fetch', fetchMock)
    await request('/api/x', { method: 'POST', json: { a: 1 } })
    const init = fetchMock.mock.calls[0][1] as RequestInit
    expect(init.body).toBe(JSON.stringify({ a: 1 }))
    expect((init.headers as Headers).get('Content-Type')).toBe('application/json')
  })
})

describe('HttpError.json', () => {
  it('parses a JSON body', () => {
    const e = new HttpError(400, JSON.stringify({ error: 'bad' }))
    expect(e.json).toEqual({ error: 'bad' })
  })

  it('returns undefined for a non-JSON body', () => {
    const e = new HttpError(400, 'plain text')
    expect(e.json).toBeUndefined()
  })
})
