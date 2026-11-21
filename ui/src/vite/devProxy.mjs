// Vite dev-server helpers for apps that consume this kit.
//
// vite.config.ts is loaded by Node, not by the app's bundler, so this file is
// plain ESM JavaScript rather than the TypeScript the rest of the kit ships.
// Types live beside it in devProxy.d.mts.

import fs from 'node:fs'
import http from 'node:http'
import https from 'node:https'
import nodePath from 'node:path'

// The brand token plugin is the one build-time helper here; see brandTokens.mjs.
export { BRAND_TOKENS_FILE, DEFAULT_BRAND_SOURCE, brandTokens, extractBrandTokens, readBrandTokens, starlightBrandCSS } from './brandTokens.mjs'

// The shared override every consuming app honours. A developer running
// several SPAs against one backend exports this once in their shell instead
// of learning each app's private variable name.
export const DEV_TARGET_ENV = 'ADMIN_PROXY_TARGET_DEV'

// The platform admin server's default port, reached over the IPv4 loopback:
// Node resolves `localhost` to ::1 first, and the server would then log every
// proxied request with clientIP ::1. The dev certificate carries 127.0.0.1
// as an IP SAN, and `secure: false` applies regardless.
const DEFAULT_TARGET = 'https://127.0.0.1:8901'

/**
 * Resolve the dev-server proxy target: the shared override first, then the
 * app's own variable if it names one, then the app's default.
 */
export function devProxyTarget(opts = {}) {
  const { fallback = DEFAULT_TARGET, env } = opts
  const shared = process.env[DEV_TARGET_ENV]
  if (shared) return shared
  if (env && process.env[env]) return process.env[env]
  return fallback
}

/**
 * Build a Vite `server.proxy` map that points every given path prefix at the
 * resolved target. `secure: false` so a self-signed dev certificate works.
 */
export function devProxy(paths, opts = {}) {
  const target = devProxyTarget(opts)
  if (target !== (opts.fallback ?? DEFAULT_TARGET)) {
    console.log(`[@bboxsys/ui] dev proxy -> ${target}`)
  }
  return Object.fromEntries(
    paths.map(path => [path, { target, secure: false, changeOrigin: true }]),
  )
}

// The endpoint-map override. An app's SPA discovers service URLs from the
// server's /serviceendpoints/ map, so pointing the dev proxy at a different
// backend does not change where the SPA signs in -- the URLs inside the map
// still win. This variable rewrites entries of that map for the dev server
// only, leaving the browser code identical to production.
//
//   BBOX_DEV_ENDPOINTS=https://localhost:9900
//   BBOX_DEV_ENDPOINTS=login_service=https://localhost:9900,aws_service=http://localhost:9910
//
// A bare URL is shorthand for the admin/login pair.
export const DEV_ENDPOINTS_ENV = 'BBOX_DEV_ENDPOINTS'

// The kinds a bare URL applies to: the admin server publishes both.
const ADMIN_KINDS = ['login_service', 'admin_service']

/** Parse the BBOX_DEV_ENDPOINTS syntax into a {kind: url} map. */
export function parseDevEndpoints(raw) {
  const value = (raw ?? '').trim()
  if (!value) return {}
  if (!value.includes('=')) {
    return Object.fromEntries(ADMIN_KINDS.map(kind => [kind, value]))
  }
  const out = {}
  for (const pair of value.split(',')) {
    const at = pair.indexOf('=')
    if (at < 0) continue
    const kind = pair.slice(0, at).trim()
    const url = pair.slice(at + 1).trim()
    if (kind && url) out[kind] = url
  }
  return out
}

// A project may keep its overrides beside its other local test settings, in
// ~/.config/<project>-test/dev-endpoints.json ({"login_service": "..."}).
// The environment variable wins over the file.
function readProjectEndpoints(project) {
  if (!project) return {}
  const home = process.env.HOME || process.env.USERPROFILE
  if (!home) return {}
  const file = nodePath.join(home, '.config', `${project}-test`, 'dev-endpoints.json')
  try {
    return JSON.parse(fs.readFileSync(file, 'utf8'))
  } catch {
    return {}
  }
}

function fetchJSON(url) {
  const client = url.startsWith('http://') ? http : https
  return new Promise((resolve, reject) => {
    const req = client.get(url, { rejectUnauthorized: false }, res => {
      let body = ''
      res.on('data', chunk => { body += chunk })
      res.on('end', () => {
        try {
          resolve(JSON.parse(body))
        } catch (err) {
          reject(err)
        }
      })
    })
    req.on('error', reject)
  })
}

/**
 * Vite plugin that serves the service endpoint map itself in development,
 * fetching it from the same target devProxy would use and applying the
 * overrides on top. Register it before the proxy handles the route:
 *
 *   plugins: [vue(), devEndpoints({ fallback: 'https://localhost:8880', env: 'MYAPP_UI_SERVER', project: 'myapp' })]
 *
 * With no overrides configured the plugin stands aside and the route is
 * proxied as usual.
 */
export function devEndpoints(opts = {}) {
  const { route = '/serviceendpoints', project, ...targetOpts } = opts
  return {
    name: '@bboxsys/ui:dev-endpoints',
    apply: 'serve',
    configureServer(server) {
      const overrides = { ...readProjectEndpoints(project), ...parseDevEndpoints(process.env[DEV_ENDPOINTS_ENV]) }
      const kinds = Object.keys(overrides)
      if (kinds.length === 0) return
      const target = devProxyTarget(targetOpts)
      const summary = kinds.map(kind => `${kind} -> ${overrides[kind]}`).join(', ')
      console.log(`[@bboxsys/ui] dev endpoints (from ${target}): ${summary}`)
      // Registered from the hook body, so it runs ahead of the proxy.
      server.middlewares.use(route, async (_req, res, next) => {
        try {
          const upstream = await fetchJSON(`${target.replace(/\/$/, '')}${route}/`)
          const map = { ...upstream }
          for (const [kind, url] of Object.entries(overrides)) {
            map[kind] = { ...(map[kind] ?? {}), kind, url }
          }
          res.setHeader('Content-Type', 'application/json')
          res.end(JSON.stringify(map))
        } catch (err) {
          next(err)
        }
      })
    },
  }
}

/**
 * Maps a file under a linked package's real directory to the path the dev
 * server knows it by (under node_modules, because apps resolve the kit with
 * preserveSymlinks). Returns null for a file outside realDir.
 */
export function linkedPath(realDir, linkedDir, file) {
  const rel = nodePath.relative(realDir, file)
  if (rel === '' || rel.startsWith('..') || nodePath.isAbsolute(rel)) return null
  return nodePath.join(linkedDir, rel)
}

/**
 * Vite plugin that hot-reloads a symlinked kit. An app that depends on this
 * kit by path (file:../../ui) sees it as a symlink under node_modules, and
 * resolves it with preserveSymlinks so module ids stay under node_modules.
 * Vite's watcher ignores node_modules, and un-ignoring the link with a
 * negated glob does not make it follow the link, so without this plugin a
 * running dev server keeps serving the kit as it was at startup (a new
 * export then fails to import and lazy routes silently do not load).
 *
 * The plugin watches the link's real source directory, maps each change
 * back to the node_modules path, invalidates those modules, and reloads the
 * page. It does nothing when the package is not a symlink (an installed
 * release never changes under a running server).
 *
 *   plugins: [vue(), watchLinkedKit()]
 */
export function watchLinkedKit(opts = {}) {
  const { packageName = '@bboxsys/ui', dir = 'src' } = opts
  return {
    name: '@bboxsys/ui:watch-linked-kit',
    apply: 'serve',
    configureServer(server) {
      const linkedRoot = nodePath.resolve(server.config.root, 'node_modules', packageName)
      let realRoot
      try {
        realRoot = fs.realpathSync(linkedRoot)
      } catch {
        return
      }
      if (realRoot === linkedRoot) return
      const realDir = nodePath.join(realRoot, dir)
      const linkedDir = nodePath.join(linkedRoot, dir)
      server.watcher.add(realDir)
      const graph = server.environments?.client?.moduleGraph ?? server.moduleGraph
      // Vite serves files under node_modules with a ?v= version and an
      // immutable cache header, so the browser keeps a module until its URL
      // changes. Stamping the module and every importer up to the entry with
      // an HMR timestamp makes each one re-transform with ?t= imports, so the
      // reload fetches the edited file under a new URL instead of the cache.
      const reload = file => {
        const id = linkedPath(realDir, linkedDir, file)
        if (!id) return
        const start = [...(graph.getModulesByFile(id) ?? [])]
        if (start.length === 0) return
        const timestamp = Date.now()
        const seen = new Set()
        const stamp = mod => {
          if (seen.has(mod)) return
          seen.add(mod)
          graph.invalidateModule(mod, new Set(), timestamp, true)
          for (const importer of mod.importers ?? []) stamp(importer)
        }
        start.forEach(stamp)
        server.ws.send({ type: 'full-reload' })
      }
      server.watcher.on('change', reload)
      server.watcher.on('add', reload)
      server.watcher.on('unlink', reload)
      console.log(`[@bboxsys/ui] watching linked ${packageName} at ${realDir}`)
    },
  }
}
