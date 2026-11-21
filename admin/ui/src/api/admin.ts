import { createAssistantClient, request, streamEvents, type RequestAuth, type RequestOptions } from '@bboxsys/ui'
import { adminBase, useSessionStore } from '@/stores/session'
import type {
  Account, AccountConfig, AccountStats, Activity, Agent, AgentConfiguration, AgentConfigurationOptions, AICredentialCheck, AIModelsInput, AIModelsResult, AICredentialKind, AIDocument, AIEvent,
  AIInstanceSummary, AISearchResult, AISession, AISessionInput, AISessionSummary, AIStatus, AIUsageGroup, AIUsageGroupBy,
  AIUsageRunsPage, AIUsageRunsQuery, APIToken, APITokenInput,
  CertificateInfo, Invitation, InvitationAcceptRequest, InvitationConfig, LogBucket, LoginResponse, LogEntry, NatsConnz,
  NatsVarz, OAuthNewUserResponse, OAuthPublicConfig, OIDCConnection, OIDCConnectionInput, PKIStatus,
  PublicInvitation, QueueRecord, Service, ServiceEndpoint, StoreMetricsEntry, User, UserConfig, UserDetail } from '@bboxsys/ui'

// The SPA is served by the admin server, so the API is always same-origin
// (or VITE_ADMIN_URL in development). The service endpoint map is only
// used to link out to other services.
function base(): string {
  return adminBase()
}

function auth(): RequestAuth {
  return useSessionStore().auth
}

function call<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  return request<T>(`${base()}${path}`, { auth: auth(), ...opts })
}

// Super admins may pass account_id to act on another tenant; the server
// ignores it for everyone else.
function scoped(accountId?: string): Record<string, string | undefined> {
  const s = useSessionStore()
  return s.isSuperAdmin && accountId && accountId !== s.accountId ? { account_id: accountId } : {}
}

export const accountsApi = {
  list: () => call<Account[]>('/api/v1/accounts'),
  me: () => call<Account>('/api/v1/accounts/me'),
  stats: () => call<Record<string, AccountStats>>('/api/v1/accounts/stats'),
  create: (cfg: AccountConfig) => call<Account>('/api/v1/accounts', { method: 'POST', json: cfg }),
  remove: (id: string) => call<void>(`/api/v1/accounts/${encodeURIComponent(id)}`, { method: 'DELETE' }),
}

export const usersApi = {
  list: (accountId?: string) => call<User[]>('/api/v1/users', { query: scoped(accountId) }),
  get: (userId: string, accountId?: string) =>
    call<UserDetail>(`/api/v1/users/${encodeURIComponent(userId)}`, { query: scoped(accountId) }),
  create: (cfg: UserConfig) => call<User>('/api/v1/users', { method: 'POST', json: cfg }),
  remove: (id: string) => call<void>(`/api/v1/users/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  unlock: (accountId: string, userId: string) =>
    call<Activity>(`/api/v1/accounts/${encodeURIComponent(accountId)}/users/${encodeURIComponent(userId)}/unlock`, { method: 'POST' }),
  activities: (accountId?: string) => call<Activity[]>('/api/v1/activities', { query: scoped(accountId) }),
  roles: () => call<string[]>('/api/v1/roles'),
}

export const meApi = {
  changePassword: (current_password: string, new_password: string) =>
    call<{ status: string }>('/api/v1/users/me/password', { method: 'PUT', json: { current_password, new_password } }),
  setTheme: (theme_preference: 'system' | 'dark' | 'light') =>
    call<{ theme_preference: string }>('/api/v1/users/me/theme', { method: 'PUT', json: { theme_preference } }),
  preferences: () => call<Record<string, string>>('/api/v1/users/me/preferences'),
  setPreference: (key: string, value: string) =>
    call<Record<string, string>>(`/api/v1/users/me/preferences/${encodeURIComponent(key)}`, { method: 'PUT', json: { value } }),
  deletePreference: (key: string) =>
    call<Record<string, string>>(`/api/v1/users/me/preferences/${encodeURIComponent(key)}`, { method: 'DELETE' }),
  uploadPhoto: (file: File) => {
    const body = new FormData()
    body.append('photo', file)
    return call<{ profile_photo: string }>('/api/v1/users/me/photo', { method: 'POST', body })
  },
  deletePhoto: () => call<void>('/api/v1/users/me/photo', { method: 'DELETE' }),
}

export const invitationsApi = {
  list: (accountId?: string) => call<Invitation[]>('/api/v1/invitations', { query: accountId ? { account_id: accountId } : {} }),
  create: (cfg: InvitationConfig) => call<Invitation>('/api/v1/invitations', { method: 'POST', json: cfg }),
  revoke: (token: string) => call<void>(`/api/v1/invitations/${encodeURIComponent(token)}`, { method: 'DELETE' }),
  // Public: no session required.
  getPublic: (token: string) => request<PublicInvitation>(`${adminBase()}/api/v1/invitations/${encodeURIComponent(token)}`),
  accept: (token: string, req: InvitationAcceptRequest) =>
    request<LoginResponse>(`${adminBase()}/api/v1/invitations/${encodeURIComponent(token)}/accept`, { method: 'POST', json: req }),
}

// Per-account OIDC/SSO connection. get() returns 404 when none is configured.
export const oidcApi = {
  get: (accountId?: string) => call<OIDCConnection>('/api/v1/oidc/connection', { query: scoped(accountId) }),
  put: (conn: OIDCConnectionInput, accountId?: string) =>
    call<OIDCConnection>('/api/v1/oidc/connection', { method: 'PUT', json: conn, query: scoped(accountId) }),
  remove: (accountId?: string) =>
    call<{ result: string }>('/api/v1/oidc/connection', { method: 'DELETE', query: scoped(accountId) }),
}

export const servicesApi = {
  list: () => call<Service[]>('/api/v1/services'),
  create: (cfg: UserConfig) => call<Service>('/api/v1/services', { method: 'POST', json: cfg }),
  remove: (id: string) => call<void>(`/api/v1/services/${encodeURIComponent(id)}`, { method: 'DELETE' }),
}

export const agentsApi = {
  list: () => call<Agent[]>('/api/v1/agents'),
  create: (cfg: UserConfig) => call<Agent>('/api/v1/agents', { method: 'POST', json: cfg }),
  remove: (id: string) => call<void>(`/api/v1/agents/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  configuration: (opts: AgentConfigurationOptions) =>
    call<AgentConfiguration>('/api/v1/agents/configuration', { method: 'POST', json: opts }),
}

export const endpointsApi = {
  list: () => call<Record<string, ServiceEndpoint>>('/api/v1/serviceendpoints'),
}

export const pkiApi = {
  status: () => call<PKIStatus>('/api/v1/pki/status'),
  certificates: () => call<CertificateInfo[]>('/api/v1/pki/certificates'),
}

export const tokensApi = {
  // Lists the caller's user-scoped tokens plus the account-scoped ones they
  // can use; create/update/remove here act on the caller's own tokens.
  list: () => call<APIToken[]>('/api/v1/apitokens'),
  create: (t: APITokenInput) => call<APIToken>('/api/v1/apitokens', { method: 'POST', json: t }),
  update: (name: string, t: Partial<APITokenInput>) => call<APIToken>(`/api/v1/apitokens/${encodeURIComponent(name)}`, { method: 'PUT', json: t }),
  remove: (name: string) => call<void>(`/api/v1/apitokens/${encodeURIComponent(name)}`, { method: 'DELETE' }),
}

// Account-scoped tokens: readable by everyone in the account, managed by
// administrators (the server returns 403 otherwise).
export const accountTokensApi = {
  list: () => call<APIToken[]>('/api/v1/accounts/me/apitokens'),
  create: (t: APITokenInput) => call<APIToken>('/api/v1/accounts/me/apitokens', { method: 'POST', json: t }),
  update: (name: string, t: Partial<APITokenInput>) => call<APIToken>(`/api/v1/accounts/me/apitokens/${encodeURIComponent(name)}`, { method: 'PUT', json: t }),
  remove: (name: string) => call<void>(`/api/v1/accounts/me/apitokens/${encodeURIComponent(name)}`, { method: 'DELETE' }),
}

export const oauthApi = {
  config: () => request<OAuthPublicConfig>(`${adminBase()}/api/v1/oauth/config`),
  authorize: (inviteToken?: string) =>
    request<{ authorization_url: string; state: string }>(`${adminBase()}/api/v1/oauth/google/authorize`, {
      query: { invite_token: inviteToken },
    }),
  callback: (code: string, state: string) =>
    request<LoginResponse | OAuthNewUserResponse>(`${adminBase()}/api/v1/oauth/google/callback`, { method: 'POST', json: { code, state } }),
  createAccount: (account_name: string, description: string, oauth_subject: string) =>
    call<LoginResponse>('/api/v1/oauth/accounts', { method: 'POST', json: { account_name, description, oauth_subject } }),
}

// A type alias (not an interface) so it satisfies the query Record type.
export type LogSummaryField = 'service' | 'host' | 'level' | 'component' | 'message'

export type LogQuery = {
  /** Page size; the server defaults to 200 when absent. */
  limit?: number
  offset?: number
  service?: string
  host?: string
  level?: string
  search?: string
  /** RFC 3339, Unix seconds or milliseconds, or a duration such as "15m" meaning that long before now. */
  since?: string
  until?: string
  sort?: 'asc' | 'desc'
}

export const opsApi = {
  logs: (q: LogQuery) => call<LogEntry[]>('/api/v1/logs', { query: q }),
  logsCount: (q: LogQuery) => call<{ count: number }>('/api/v1/logs/count', { query: { service: q.service, host: q.host, level: q.level, search: q.search, since: q.since, until: q.until } }),
  logsSummary: (q: LogQuery & { by?: LogSummaryField; limit?: number }) =>
    call<{ by: LogSummaryField; buckets: LogBucket[] }>('/api/v1/logs/summary', { query: { by: q.by, limit: q.limit, service: q.service, host: q.host, level: q.level, search: q.search, since: q.since, until: q.until } }),
  logLevel: () => call<{ level: string }>('/api/v1/config/loglevel'),
  setLogLevel: (level: string) => call<{ level: string }>('/api/v1/config/loglevel', { method: 'PUT', json: { level } }),
  logMaxEntries: () => call<{ max_entries: number }>('/api/v1/config/logmaxentries'),
  setLogMaxEntries: (max_entries: number) => call<{ max_entries: number }>('/api/v1/config/logmaxentries', { method: 'PUT', json: { max_entries } }),
  storeMetrics: () => call<StoreMetricsEntry[]>('/api/v1/metrics/stores'),
  storeMetricsHistory: (q: { limit?: number; offset?: number; service?: string; store_name?: string; sort?: 'asc' | 'desc' }) =>
    call<StoreMetricsEntry[]>('/api/v1/metrics/stores/history', { query: q }),
  connz: () => call<NatsConnz>('/api/v1/services/status'),
  varz: () => call<NatsVarz>('/api/v1/services/varz'),
  queueJobs: () => call<QueueRecord[]>('/api/v1/queue'),
  queueJob: (id: string) => call<QueueRecord>(`/api/v1/queue/${encodeURIComponent(id)}`),
}

// The AI assistant hosted by the admin server. Sessions belong to the
// caller; sendMessage streams the run as server-sent events because the
// kit's request helper waits for the whole body.
export const aiApi = {
  status: () => call<AIStatus>('/api/v1/ai/status'),
  sessions: {
    list: () => call<AISessionSummary[]>('/api/v1/ai/sessions'),
    create: (input: AISessionInput = {}) => call<AISession>('/api/v1/ai/sessions', { method: 'POST', json: input }),
    get: (id: string) => call<AISession>(`/api/v1/ai/sessions/${encodeURIComponent(id)}`),
    // Renames a session; an empty title clears it. 409 while a run is in progress.
    rename: (id: string, title: string) =>
      call<AISessionSummary>(`/api/v1/ai/sessions/${encodeURIComponent(id)}`, { method: 'PATCH', json: { title } }),
    remove: (id: string) => call<void>(`/api/v1/ai/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  },
  documents: {
    add: (documents: AIDocument[]) => call<{ added: number }>('/api/v1/ai/documents', { method: 'POST', json: { documents } }),
    search: (q: string, n?: number) => call<AISearchResult[]>('/api/v1/ai/documents/search', { query: { q, n } }),
  },
  // Checks a stored AI credential against its provider without running the
  // assistant. Resolves the same name the assistant would: the caller's own
  // credential first, then the account's.
  testCredential: (name: string, kind: AICredentialKind) =>
    call<AICredentialCheck>('/api/v1/ai/credentials/test', { method: 'POST', json: { name, kind } }),
  // Lists the models a provider's endpoint serves, for the credential form.
  // The backend makes the call, so a remote endpoint's key never has to be
  // reachable from the browser, and a saved credential's key is used when
  // none is typed.
  listModels: (input: AIModelsInput) =>
    call<AIModelsResult>('/api/v1/ai/credentials/models', { method: 'POST', json: input }),
  // Usage reported by every AI instance on the platform (super admins).
  usage: {
    instances: () => call<AIInstanceSummary[]>('/api/v1/ai/usage/instances'),
    runs: (q: AIUsageRunsQuery = {}) => call<AIUsageRunsPage>('/api/v1/ai/usage/runs', { query: { ...q } }),
    summary: (since?: string, group?: AIUsageGroupBy) =>
      call<AIUsageGroup[]>('/api/v1/ai/usage/summary', { query: { since, group } }),
  },
  sendMessage: (id: string, content: string, onEvent: (e: AIEvent) => void, signal?: AbortSignal) =>
    streamEvents(`${base()}/api/v1/ai/sessions/${encodeURIComponent(id)}/messages`, auth(), { content }, onEvent, signal),
}

// The assistants the console can talk to and the client for each: the
// kit's (platform docs/AI.md sections 12 and 17), bound to the admin
// server same-origin with the session's credentials.
export const assistantClient = createAssistantClient({ base, auth })
