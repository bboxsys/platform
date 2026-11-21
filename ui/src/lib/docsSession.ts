import { request, type RequestAuth } from './http'

// The browser half of the documentation gate (platform docsite package).
// A product serves its built documentation site under a base path
// ('/docs') behind a session cookie, because a link in a chat reply or a
// bookmark carries no bearer. The SPA establishes that session once with
// the user's bearer (openDocsSession), then plain navigation to any page
// under the base works; closeDocsSession ends it at sign-out.

export const DEFAULT_DOCS_BASE = '/docs'

function trimBase(base: string): string {
  const trimmed = base.replace(/\/+$/, '')
  return trimmed === '' ? '' : trimmed.startsWith('/') || /^https?:\/\//.test(trimmed) ? trimmed : `/${trimmed}`
}

/** The session route under a base: '/docs/_session'. */
export function docsSessionUrl(base: string = DEFAULT_DOCS_BASE): string {
  return `${trimBase(base)}/_session`
}

/**
 * Establish the documentation session with the user's bearer. The cookie
 * is set by the server; `credentials: 'include'` lets a dev server on
 * another origin keep it too.
 */
export async function openDocsSession(auth: RequestAuth, base: string = DEFAULT_DOCS_BASE): Promise<void> {
  await request<void>(docsSessionUrl(base), { method: 'POST', auth, credentials: 'include' })
}

/** End the documentation session. Failures are ignored: the cookie expires on its own. */
export async function closeDocsSession(base: string = DEFAULT_DOCS_BASE): Promise<void> {
  try {
    await request<void>(docsSessionUrl(base), { method: 'DELETE', credentials: 'include' })
  } catch {
    // nothing to do
  }
}

/**
 * The page to open after the gate: `next` when it is a path under the base
 * (what the server's redirect sends), otherwise the site root. Anything
 * else, including another origin, is refused so the gate cannot be used as
 * an open redirect.
 */
export function docsTarget(next: unknown, base: string = DEFAULT_DOCS_BASE): string {
  const b = trimBase(base) || '/'
  const root = b === '/' ? '/' : `${b}/`
  if (typeof next !== 'string' || next === '') return root
  if (next.startsWith('//') || /^[a-z][a-z0-9+.-]*:/i.test(next)) return root
  if (next === b || next === root || next.startsWith(root)) return next
  return root
}
