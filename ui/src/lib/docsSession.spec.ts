import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { closeDocsSession, docsSessionUrl, docsTarget, openDocsSession } from './docsSession'

describe('docsSessionUrl', () => {
  it('builds the session route under the base', () => {
    expect(docsSessionUrl()).toBe('/docs/_session')
    expect(docsSessionUrl('/docs/')).toBe('/docs/_session')
    expect(docsSessionUrl('help')).toBe('/help/_session')
    expect(docsSessionUrl('https://host:9680/docs')).toBe('https://host:9680/docs/_session')
  })
})

describe('docsTarget', () => {
  it('keeps paths under the base and refuses the rest', () => {
    expect(docsTarget('/docs/planning/retirement/?x=1')).toBe('/docs/planning/retirement/?x=1')
    expect(docsTarget('/docs')).toBe('/docs')
    expect(docsTarget('/docs/')).toBe('/docs/')
    expect(docsTarget(undefined)).toBe('/docs/')
    expect(docsTarget('')).toBe('/docs/')
    expect(docsTarget('/dashboard')).toBe('/docs/')
    expect(docsTarget('/docsx/')).toBe('/docs/')
    expect(docsTarget('//evil.test/docs/')).toBe('/docs/')
    expect(docsTarget('https://evil.test/docs/')).toBe('/docs/')
    expect(docsTarget('javascript:alert(1)')).toBe('/docs/')
    expect(docsTarget('/help/a/', '/help')).toBe('/help/a/')
  })
})

describe('openDocsSession / closeDocsSession', () => {
  const fetchMock = vi.fn()
  beforeEach(() => {
    fetchMock.mockReset()
    vi.stubGlobal('fetch', fetchMock)
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('posts the bearer and account with credentials', async () => {
    fetchMock.mockResolvedValue(new Response('', { status: 204 }))
    await openDocsSession({ token: 't0k', accountId: 'ACC' })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/docs/_session')
    expect(init.method).toBe('POST')
    expect(init.credentials).toBe('include')
    const h = init.headers as Headers
    expect(h.get('Authorization')).toBe('Bearer t0k')
    expect(h.get('X-Blackbox-AccountID')).toBe('ACC')
  })

  it('surfaces a refused session', async () => {
    fetchMock.mockResolvedValue(new Response('forbidden', { status: 403 }))
    await expect(openDocsSession({ token: 't0k' })).rejects.toThrow('403')
  })

  it('deletes quietly', async () => {
    fetchMock.mockResolvedValue(new Response('', { status: 500 }))
    await expect(closeDocsSession('/docs')).resolves.toBeUndefined()
    expect((fetchMock.mock.calls[0] as [string, RequestInit])[1].method).toBe('DELETE')
  })
})
