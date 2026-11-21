import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { nextTick } from 'vue'
import { createSessionStore } from './createSessionStore'
import type { LoginResponse } from '../types/admin'

const endpoints = {
  admin_service: { kind: 'admin_service', url: 'https://admin.example:8901', health: {} },
  login_service: { kind: 'login_service', url: 'https://admin.example:8901', health: {} },
}

const loginResponse = {
  user: { id: 'acct:me@example.com', account_id: 'acct', email: 'me@example.com', roles: ['administrator'], theme_preference: 'dark' },
  token: 'jwt-token',
  is_super_admin: false,
  service_endpoints: endpoints,
} as unknown as LoginResponse

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

// happy-dom does not expose localStorage as a global; a Map-backed stand-in
// is enough to verify persistence.
function memoryStorage(): Storage {
  const m = new Map<string, string>()
  return {
    get length() { return m.size },
    clear: () => m.clear(),
    getItem: (k: string) => m.get(k) ?? null,
    key: (i: number) => [...m.keys()][i] ?? null,
    removeItem: (k: string) => { m.delete(k) },
    setItem: (k: string, v: string) => { m.set(k, v) },
  }
}

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  setActivePinia(createPinia())
  vi.stubGlobal('localStorage', memoryStorage())
  document.documentElement.classList.remove('dark')
  fetchMock = vi.fn()
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('createSessionStore (endpoint discovery)', () => {
  it('discovers the login service, signs in with basic auth, and persists the session', async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse(endpoints))
      .mockResolvedValueOnce(jsonResponse(loginResponse))
    const useStore = createSessionStore({ storageKey: 'test.session', serverName: 'test' })
    const s = useStore()

    expect(s.isAuthenticated).toBe(false)
    await s.login('me@example.com', 'pw')

    expect(fetchMock.mock.calls[0][0]).toBe('/serviceendpoints/')
    const [loginUrl, init] = fetchMock.mock.calls[1] as [string, RequestInit]
    expect(loginUrl).toBe('https://admin.example:8901/api/v1/login')
    expect((init.headers as Headers).get('Authorization')).toMatch(/^Basic /)

    expect(s.isAuthenticated).toBe(true)
    expect(s.accountId).toBe('acct')
    expect(s.isTenantAdmin).toBe(true)
    expect(s.canAdminister).toBe(true)
    expect(s.auth).toEqual({ token: 'jwt-token', accountId: 'acct' })
    expect(s.endpoint('admin_service')).toBe('https://admin.example:8901')
    expect(s.session?.loginService).toBe('https://admin.example:8901')
    expect(JSON.parse(localStorage.getItem('test.session') ?? 'null')?.token).toBe('jwt-token')
    expect(document.documentElement.classList.contains('dark')).toBe(true)
  })

  it('fails clearly when no login service is published', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({}))
    const s = createSessionStore({ storageKey: 'test.session', serverName: 'widget' })()
    await expect(s.login('me@example.com', 'pw')).rejects.toThrow('Is the widget server running?')
  })

  it('restores a saved session and clears it on logout', async () => {
    localStorage.setItem('test.session', JSON.stringify({
      user: loginResponse.user, token: 't', accountId: 'acct', isSuperAdmin: true, loginService: '', serviceEndpoints: endpoints,
    }))
    const s = createSessionStore({ storageKey: 'test.session' })()
    expect(s.isAuthenticated).toBe(true)
    expect(s.isSuperAdmin).toBe(true)
    expect(s.endpoint('login_service')).toBe('https://admin.example:8901')
    s.logout()
    expect(s.isAuthenticated).toBe(false)
    await nextTick() // the storage watcher flushes asynchronously
    expect(localStorage.getItem('test.session')).toBeNull()
  })

  it('patches the user in place', async () => {
    const s = createSessionStore({ storageKey: 'test.session' })()
    s.adopt(loginResponse)
    s.patchUser({ profile_photo: 'p.png' })
    expect(s.user?.profile_photo).toBe('p.png')
    await nextTick()
    expect(JSON.parse(localStorage.getItem('test.session') ?? '{}').user.profile_photo).toBe('p.png')
  })
})

describe('createSessionStore (adminBase)', () => {
  it('talks to the admin server directly and accepts an empty same-origin base', async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse(endpoints))
      .mockResolvedValueOnce(jsonResponse(loginResponse))
    const s = createSessionStore({ storageKey: 'admin.session', adminBase: () => '' })()
    await s.login('me@example.com', 'pw')
    expect(fetchMock.mock.calls[0][0]).toBe('/serviceendpoints/')
    expect(fetchMock.mock.calls[1][0]).toBe('/api/v1/login')
    expect(s.session?.loginService).toBe('')
  })

  it('prefixes every URL with a remote admin base', async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse(endpoints))
      .mockResolvedValueOnce(jsonResponse(loginResponse))
    const s = createSessionStore({ storageKey: 'admin.session', adminBase: () => 'https://remote:8901' })()
    await s.login('me@example.com', 'pw')
    expect(fetchMock.mock.calls[0][0]).toBe('https://remote:8901/serviceendpoints/')
    expect(fetchMock.mock.calls[1][0]).toBe('https://remote:8901/api/v1/login')
  })
})
