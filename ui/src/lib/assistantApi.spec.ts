import { afterEach, describe, expect, it, vi } from 'vitest'
import { ADMIN_ASSISTANT, ALL_ASSISTANT, assistantPath, createAssistantClient, sseParser } from './assistantApi'
import type { AIEvent } from '../types/admin'

describe('sseParser', () => {
  it('dispatches one event per frame, across chunk boundaries and CRLF', () => {
    const events: AIEvent[] = []
    const p = sseParser(e => events.push(e))
    p.push('event: text\r\ndata: {"type":"text","te')
    p.push('xt":"Hel"}\r\n\r\n: ping\n\ndata: {"type":"usage",\ndata: "usage":{"input_tokens":1}}\n\n')
    p.push('data: {"type":"done","stop_reason":"end_turn"}')
    expect(events.map(e => e.type)).toEqual(['text', 'usage'])
    p.end()
    expect(events.map(e => e.type)).toEqual(['text', 'usage', 'done'])
    expect(events[0].text).toBe('Hel')
    expect(events[1].usage?.input_tokens).toBe(1)
  })
})

describe('createAssistantClient', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('routes the admin assistant directly and every other through the gateway', () => {
    expect(assistantPath(ADMIN_ASSISTANT.id)).toBe('/api/v1/ai')
    expect(assistantPath(ALL_ASSISTANT.id)).toBe('/api/v1/ai/services/all')
    expect(assistantPath('kubernetes')).toBe('/api/v1/ai/services/kubernetes')
  })

  it('lists the admin alone when the server has no gateway', async () => {
    const calls: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      calls.push(url)
      return new Response('not found', { status: 404 })
    }))
    const client = createAssistantClient({ base: () => 'https://admin.example', auth: () => ({ token: 't', accountId: 'A' }) })
    expect(await client.services()).toEqual([ADMIN_ASSISTANT])
    expect(calls).toEqual(['https://admin.example/api/v1/ai/services'])
  })

  it('sends the session credentials with each call', async () => {
    let seen: Headers | undefined
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init: RequestInit) => {
      seen = new Headers(init.headers)
      return new Response(JSON.stringify([{ id: 'admin', name: 'Admin' }, { id: 'all', name: 'All' }]), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    const client = createAssistantClient({ base: () => '', auth: () => ({ token: 'tok', accountId: 'ACC' }) })
    const list = await client.services()
    expect(list.map(s => s.id)).toEqual(['admin', 'all'])
    expect(seen?.get('Authorization')).toBe('Bearer tok')
    expect(seen?.get('X-Blackbox-AccountID')).toBe('ACC')
  })
})
