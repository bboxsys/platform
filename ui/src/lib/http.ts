import { ref } from 'vue'

// Number of in-flight requests; the app shell renders the global progress
// bar from this. Nothing else should show its own loading bar.
export const outstanding = ref(0)

export class HttpError extends Error {
  status: number
  body: string

  constructor(status: number, body: string, message?: string) {
    super(message ?? `${status}: ${body || 'request failed'}`)
    this.status = status
    this.body = body
  }

  get json(): unknown {
    try {
      return JSON.parse(this.body)
    } catch {
      return undefined
    }
  }
}

export interface RequestAuth {
  token?: string
  accountId?: string
  basic?: { email: string; password: string }
}

// A handler apps register to react to an expired/invalid session. It fires when
// a bearer-authenticated request returns 401 (a basic-auth login returning 401
// is a wrong password, not an expired session, so it does not fire). Apps use
// it to clear the session and redirect to the login page.
type UnauthorizedHandler = () => void
let onUnauthorized: UnauthorizedHandler | undefined

export function setUnauthorizedHandler(fn: UnauthorizedHandler | undefined): void {
  onUnauthorized = fn
}

export interface RequestOptions extends RequestInit {
  auth?: RequestAuth
  json?: unknown
  query?: Record<string, string | number | boolean | undefined>
}

// utf8Base64 base64-encodes a string via its UTF-8 bytes. btoa alone only
// accepts Latin-1 and throws on characters like "é" or emoji, which would make
// basic-auth login fail for otherwise valid passwords.
function utf8Base64(s: string): string {
  const bytes = new TextEncoder().encode(s)
  let binary = ''
  for (const b of bytes) binary += String.fromCharCode(b)
  return btoa(binary)
}

// Note: request() attaches the auth token/credentials to whatever URL it is
// given. Only call it with first-party API base URLs, never with a URL that
// originates from server data or user input, or the bearer token would be sent
// to that origin.
function buildHeaders(auth: RequestAuth | undefined, hasJSON: boolean): Headers {
  const h = new Headers()
  if (hasJSON) h.set('Content-Type', 'application/json')
  if (auth?.token) {
    h.set('Authorization', `Bearer ${auth.token}`)
    if (auth.accountId) h.set('X-Blackbox-AccountID', auth.accountId)
  } else if (auth?.basic) {
    h.set('Authorization', `Basic ${utf8Base64(`${auth.basic.email}:${auth.basic.password}`)}`)
  }
  return h
}

export function withQuery(url: string, query?: RequestOptions['query']): string {
  if (!query) return url
  const p = new URLSearchParams()
  for (const [k, v] of Object.entries(query)) {
    // Only undefined means "omit"; an empty string is a real value (e.g.
    // clearing a server-side filter) and is sent as k=.
    if (v !== undefined) p.set(k, String(v))
  }
  const qs = p.toString()
  return qs ? `${url}${url.includes('?') ? '&' : '?'}${qs}` : url
}

export async function request<T>(url: string, opts: RequestOptions = {}): Promise<T> {
  const { auth, json, query, headers: extraHeaders, ...init } = opts
  const headers = buildHeaders(auth, json !== undefined)
  if (extraHeaders) new Headers(extraHeaders).forEach((v, k) => headers.set(k, v))
  outstanding.value++
  try {
    const res = await fetch(withQuery(url, query), {
      ...init,
      headers,
      body: json !== undefined ? JSON.stringify(json) : init.body,
    })
    const text = await res.text()
    if (!res.ok) {
      // Only a bearer-authenticated 401 signals an expired session; a basic-auth
      // login 401 is a failed sign-in and must not trigger a logout/redirect.
      if (res.status === 401 && auth?.token) onUnauthorized?.()
      throw new HttpError(res.status, text)
    }
    if (!text) return undefined as T
    try {
      return JSON.parse(text) as T
    } catch {
      // A 2xx response with a non-JSON body (e.g. a proxy returning an HTML
      // error page) should surface as a clear HttpError, not a raw SyntaxError.
      throw new HttpError(res.status, text, 'expected a JSON response but received a non-JSON body')
    }
  } finally {
    outstanding.value--
  }
}
