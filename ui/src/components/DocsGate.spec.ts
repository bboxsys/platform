import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import DocsGate from './DocsGate.vue'

describe('DocsGate', () => {
  const fetchMock = vi.fn()
  beforeEach(() => {
    fetchMock.mockReset()
    vi.stubGlobal('fetch', fetchMock)
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('opens the session and continues to the requested page', async () => {
    fetchMock.mockResolvedValue(new Response('', { status: 204 }))
    const navigate = vi.fn()
    mount(DocsGate, { props: { auth: { token: 't', accountId: 'A' }, next: '/docs/planning/retirement/#x', navigate } })
    await flushPromises()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect((fetchMock.mock.calls[0] as [string])[0]).toBe('/docs/_session')
    expect(navigate).toHaveBeenCalledWith('/docs/planning/retirement/#x')
  })

  it('falls back to the site root for a foreign next', async () => {
    fetchMock.mockResolvedValue(new Response('', { status: 204 }))
    const navigate = vi.fn()
    mount(DocsGate, { props: { auth: { token: 't' }, next: 'https://evil.test/', navigate } })
    await flushPromises()
    expect(navigate).toHaveBeenCalledWith('/docs/')
  })

  it('shows a refusal and offers a retry', async () => {
    fetchMock.mockResolvedValue(new Response('forbidden', { status: 403 }))
    const navigate = vi.fn()
    const w = mount(DocsGate, { props: { auth: { token: 't' }, navigate } })
    await flushPromises()
    expect(navigate).not.toHaveBeenCalled()
    expect(w.text()).toContain('not allowed')
    expect(w.emitted('error')?.length).toBe(1)
    fetchMock.mockResolvedValue(new Response('', { status: 204 }))
    await w.get('button').trigger('click')
    await flushPromises()
    expect(navigate).toHaveBeenCalledWith('/docs/')
  })
})
