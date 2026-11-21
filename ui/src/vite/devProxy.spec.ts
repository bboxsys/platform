import { beforeEach, describe, expect, it } from 'vitest'
// Plain ESM JavaScript; its types live in devProxy.d.mts.
import { DEV_ENDPOINTS_ENV, DEV_TARGET_ENV, devEndpoints, devProxy, devProxyTarget, linkedPath, parseDevEndpoints, watchLinkedKit } from './devProxy.mjs'

const APP_ENV = 'ADMIN_PROXY_TARGET'

// The kit's tsconfig deliberately carries no node types, so reach the
// process environment through globalThis rather than pulling them in.
const env = (globalThis as unknown as { process: { env: Record<string, string | undefined> } }).process.env

describe('devProxyTarget', () => {
  beforeEach(() => {
    delete env[DEV_TARGET_ENV]
    delete env[APP_ENV]
  })

  it('falls back to the app default when nothing is set', () => {
    expect(devProxyTarget({ fallback: 'https://localhost:8880', env: APP_ENV })).toBe('https://localhost:8880')
  })

  it('uses the app variable when it is set', () => {
    env[APP_ENV] = 'https://localhost:9000'
    expect(devProxyTarget({ fallback: 'https://localhost:8880', env: APP_ENV })).toBe('https://localhost:9000')
  })

  it('lets the shared override win over the app variable', () => {
    env[APP_ENV] = 'https://localhost:9000'
    env[DEV_TARGET_ENV] = 'https://localhost:9900'
    expect(devProxyTarget({ fallback: 'https://localhost:8880', env: APP_ENV })).toBe('https://localhost:9900')
  })

  it('works for an app that names no variable of its own', () => {
    env[DEV_TARGET_ENV] = 'https://localhost:9900'
    expect(devProxyTarget({ fallback: 'https://localhost:8880' })).toBe('https://localhost:9900')
  })
})

describe('devProxy', () => {
  beforeEach(() => {
    delete env[DEV_TARGET_ENV]
  })

  it('points every path at the resolved target', () => {
    env[DEV_TARGET_ENV] = 'https://localhost:9900'
    expect(devProxy(['/api', '/serviceendpoints'], { fallback: 'https://localhost:8901' })).toEqual({
      '/api': { target: 'https://localhost:9900', secure: false, changeOrigin: true },
      '/serviceendpoints': { target: 'https://localhost:9900', secure: false, changeOrigin: true },
    })
  })
})

describe('parseDevEndpoints', () => {
  it('returns nothing for an unset or blank value', () => {
    expect(parseDevEndpoints(undefined)).toEqual({})
    expect(parseDevEndpoints('  ')).toEqual({})
  })

  it('treats a bare URL as the admin/login pair', () => {
    expect(parseDevEndpoints('https://localhost:9900')).toEqual({
      login_service: 'https://localhost:9900',
      admin_service: 'https://localhost:9900',
    })
  })

  it('parses explicit kind=url pairs', () => {
    expect(parseDevEndpoints('login_service=https://localhost:9900,aws_service=http://localhost:9910')).toEqual({
      login_service: 'https://localhost:9900',
      aws_service: 'http://localhost:9910',
    })
  })

  it('keeps the scheme separator out of the kind', () => {
    expect(parseDevEndpoints('login_service=https://host:1/x=y')).toEqual({
      login_service: 'https://host:1/x=y',
    })
  })

  it('ignores malformed pairs', () => {
    expect(parseDevEndpoints('login_service=https://localhost:9900,garbage,=,x=')).toEqual({
      login_service: 'https://localhost:9900',
    })
  })
})

describe('devEndpoints', () => {
  beforeEach(() => {
    delete env[DEV_ENDPOINTS_ENV]
  })

  it('stands aside when nothing is configured', () => {
    const used: string[] = []
    const server = { middlewares: { use: (route: string) => used.push(route) } }
    devEndpoints({ fallback: 'https://localhost:8880' }).configureServer(server)
    expect(used).toEqual([])
  })

  it('serves the route itself when overrides are set', () => {
    env[DEV_ENDPOINTS_ENV] = 'https://localhost:9900'
    const used: string[] = []
    const server = { middlewares: { use: (route: string) => used.push(route) } }
    devEndpoints({ fallback: 'https://localhost:8880' }).configureServer(server)
    expect(used).toEqual(['/serviceendpoints'])
  })
})

describe('watchLinkedKit', () => {
  it('maps a file under the real directory to its linked path', () => {
    expect(linkedPath('/w/ui/src', '/w/app/node_modules/@bboxsys/ui/src', '/w/ui/src/lib/aiCredential.ts'))
      .toBe('/w/app/node_modules/@bboxsys/ui/src/lib/aiCredential.ts')
  })

  it('ignores files outside the real directory and the directory itself', () => {
    expect(linkedPath('/w/ui/src', '/w/app/node_modules/@bboxsys/ui/src', '/w/ui/package.json')).toBeNull()
    expect(linkedPath('/w/ui/src', '/w/app/node_modules/@bboxsys/ui/src', '/w/ui/srcx/a.ts')).toBeNull()
    expect(linkedPath('/w/ui/src', '/w/app/node_modules/@bboxsys/ui/src', '/w/ui/src')).toBeNull()
  })

  it('is a serve-only plugin', () => {
    const plugin = watchLinkedKit()
    expect(plugin.apply).toBe('serve')
    expect(plugin.name).toBe('@bboxsys/ui:watch-linked-kit')
  })
})
