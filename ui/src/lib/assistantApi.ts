import { HttpError, request, type RequestAuth, type RequestOptions } from './http'
import type { AIEvent, AISession, AISessionInput, AISessionSummary, AIStatus, AssistantService } from '../types/admin'

// The browser half of the assistant conversation API (platform docs/AI.md
// sections 12 and 17), for any SPA that signs in with the session store:
// the admin console reaches the admin server same-origin, a product SPA
// reaches it at the admin_service endpoint. Every call carries the
// caller's bearer, so the assistant acts as the signed-in user.

// The admin's own assistant, served at /api/v1/ai; every other assistant is
// relayed at /api/v1/ai/services/{id}.
export const ADMIN_ASSISTANT: AssistantService = { id: 'admin', name: 'Admin' }
// The coordinator the gateway hosts once a product registered a service
// assistant: it answers by asking the other assistants, each as the caller.
export const ALL_ASSISTANT: AssistantService = { id: 'all', name: 'All' }

export interface AssistantClientOptions {
  // The admin server's base URL ('' for same-origin).
  base: () => string
  // The session's credentials: bearer and account.
  auth: () => RequestAuth
}

// A client bound to one assistant for its whole life, so a request started
// on one assistant cannot land in another's conversation store.
export interface AssistantApi {
  service: string
  status: () => Promise<AIStatus>
  sessions: {
    list: () => Promise<AISessionSummary[]>
    create: (input?: AISessionInput) => Promise<AISession>
    get: (id: string) => Promise<AISession>
    // Renames a session; an empty title clears it. 409 while a run is in progress.
    rename: (id: string, title: string) => Promise<AISessionSummary>
    remove: (id: string) => Promise<void>
  }
  // Streams one run as server-sent events, handing each event to onEvent
  // as it arrives. Aborting the signal ends the run; the AbortError
  // propagates to the caller.
  sendMessage: (id: string, content: string, onEvent: (e: AIEvent) => void, signal?: AbortSignal) => Promise<void>
}

export interface AssistantClient {
  // The assistants the caller can talk to: the admin's own first, then the
  // registered services by name, then All when there is more than one. A
  // server without the gateway (an older admin) answers 404, which reads as
  // the admin alone.
  services: () => Promise<AssistantService[]>
  // The client for one assistant by id.
  for: (service: string) => AssistantApi
}

export function assistantPath(service: string): string {
  return service === ADMIN_ASSISTANT.id ? '/api/v1/ai' : `/api/v1/ai/services/${encodeURIComponent(service)}`
}

export function createAssistantClient(opts: AssistantClientOptions): AssistantClient {
  function call<T>(path: string, o: RequestOptions = {}): Promise<T> {
    return request<T>(`${opts.base()}${path}`, { auth: opts.auth(), ...o })
  }
  return {
    async services() {
      try {
        const list = await call<AssistantService[] | null>('/api/v1/ai/services')
        return list?.length ? list : [ADMIN_ASSISTANT]
      } catch (e) {
        if (e instanceof HttpError && e.status === 404) return [ADMIN_ASSISTANT]
        throw e
      }
    },
    for(service: string): AssistantApi {
      const path = assistantPath(service)
      return {
        service,
        status: () => call<AIStatus>(`${path}/status`),
        sessions: {
          list: () => call<AISessionSummary[] | null>(`${path}/sessions`).then(r => r ?? []),
          create: (input: AISessionInput = {}) => call<AISession>(`${path}/sessions`, { method: 'POST', json: input }),
          get: (id: string) => call<AISession>(`${path}/sessions/${encodeURIComponent(id)}`),
          rename: (id: string, title: string) =>
            call<AISessionSummary>(`${path}/sessions/${encodeURIComponent(id)}`, { method: 'PATCH', json: { title } }),
          remove: (id: string) => call<void>(`${path}/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' }),
        },
        sendMessage: (id, content, onEvent, signal) =>
          streamEvents(`${opts.base()}${path}/sessions/${encodeURIComponent(id)}/messages`, opts.auth(), { content }, onEvent, signal),
      }
    },
  }
}

// The headers the request helper sends for a session: the bearer and, with
// it, the account the caller acts in.
function streamHeaders(auth: RequestAuth): Headers {
  const h = new Headers({ 'Content-Type': 'application/json', Accept: 'text/event-stream' })
  if (auth.token) {
    h.set('Authorization', `Bearer ${auth.token}`)
    if (auth.accountId) h.set('X-Blackbox-AccountID', auth.accountId)
  }
  return h
}

// POSTs json to an SSE route and hands every frame's data, parsed as JSON,
// to onEvent as it arrives. A non-2xx status throws an HttpError carrying
// the body, as the request helper does.
export async function streamEvents(url: string, auth: RequestAuth, json: unknown, onEvent: (e: AIEvent) => void, signal?: AbortSignal): Promise<void> {
  const res = await fetch(url, { method: 'POST', headers: streamHeaders(auth), body: JSON.stringify(json), signal })
  if (!res.ok || !res.body) {
    throw new HttpError(res.status, await res.text())
  }
  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  const parser = sseParser(onEvent)
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    parser.push(decoder.decode(value, { stream: true }))
  }
  parser.push(decoder.decode())
  parser.end()
}

// Splits an event stream into frames and dispatches each frame's data
// lines, joined, as one JSON event. Frames end at a blank line; CRLF line
// endings are normalized; comment lines (": ping") carry nothing.
export function sseParser(onEvent: (e: AIEvent) => void) {
  let buffer = ''
  const dispatch = (frame: string) => {
    const data: string[] = []
    for (const line of frame.split('\n')) {
      // Only data lines carry the event; the type travels inside the JSON
      // as well as in the event: line, so the latter is not needed.
      if (line.startsWith('data:')) data.push(line.slice(5).replace(/^ /, ''))
    }
    if (data.length === 0) return
    onEvent(JSON.parse(data.join('\n')) as AIEvent)
  }
  return {
    push(chunk: string) {
      buffer += chunk
      buffer = buffer.replace(/\r\n/g, '\n')
      let at: number
      while ((at = buffer.indexOf('\n\n')) >= 0) {
        const frame = buffer.slice(0, at)
        buffer = buffer.slice(at + 2)
        dispatch(frame)
      }
    },
    end() {
      if (buffer.trim()) dispatch(buffer)
      buffer = ''
    },
  }
}
