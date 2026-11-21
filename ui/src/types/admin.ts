// Wire types of the platform admin server API, transcribed from the Go types
// in github.com/bboxsys/platform (admin/, user/, pki/, logentry/,
// storemetrics/). Shared by every SPA that signs in through the admin server.
// When a Go type changes, update it here and release the kit.

export type Role = 'administrator' | 'user' | 'viewer'
export const ROLES: Role[] = ['administrator', 'user', 'viewer']

// OIDCConnection mirrors the server's public OIDC connection shape (no secret).
export interface OIDCConnection {
  account_id: string
  slug: string
  enabled: boolean
  issuer_url: string
  client_id: string
  redirect_uri: string
  scopes: string[]
  groups_claim: string
  group_role_mappings: Record<string, string>
  auto_provision: boolean
  default_roles: string[]
  created_at?: string
  updated_at?: string
}

// OIDCConnectionInput is the write payload. client_secret is optional: omit it
// to keep the stored secret (the server never returns it).
export interface OIDCConnectionInput {
  slug: string
  enabled: boolean
  issuer_url: string
  client_id: string
  client_secret?: string
  redirect_uri: string
  scopes: string[]
  groups_claim: string
  group_role_mappings: Record<string, string>
  auto_provision: boolean
  default_roles: string[]
}

export interface UserBase {
  id: string
  created_at: string
  roles: string[]
  publicKey: string
  claims: unknown
  token: string
  seed: string
  account_id: string
}

export interface User extends UserBase {
  email: string
  firstname: string
  lastname: string
  oauth_provider?: string
  oauth_subject?: string
  picture?: string
  profile_photo?: string
  theme_preference?: 'system' | 'dark' | 'light' | ''
  preferences?: Record<string, string>
}

export interface Service extends UserBase {
  name: string
}

export interface Agent extends UserBase {
  host_id: string
  hostname: string
}

export interface UserConfig {
  email?: string
  firstname?: string
  lastname?: string
  password?: string
  roles?: string[]
  account_id?: string
  name?: string
  host_id?: string
  hostname?: string
}

export interface Activity {
  id: string
  last_ip: string
  last_ipv4: string
  active: boolean
  login_count: number
  failed_login_count: number
  last_failed_login: string
  locked: boolean
  locked_at: string
  loggedin: boolean
  last_login: string
  last_activity: string
}

// The paged form of an admin list route (GET /api/v1/accounts, users,
// activities, services, agents, serviceendpoints, pki/certificates,
// apitokens, accounts/me/apitokens, invitations). A request that names
// limit (1 to 200) or offset gets this instead of the complete array;
// total counts the whole collection and next_offset is present while
// has_more is true.
export interface Page {
  total: number
  limit: number
  offset: number
  has_more: boolean
  next_offset?: number
}

export interface ListPage<T> extends Page {
  items: T[]
}

export interface Account {
  id: string
  created_at: string
  name: string
  description: string
  owner: string
  seed: string
  public_key: string
  token: string
  claims: unknown
}

export interface AccountConfig {
  name: string
  description: string
  owner: string
  operator_id?: string
}

export interface AccountStats {
  account_id: string
  user_count: number
  active_count: number
}

export type InvitationStatus = 'pending' | 'accepted' | 'expired' | 'revoked'

export interface Invitation {
  id: string
  token: string
  account_id: string
  invited_by: string
  email: string
  roles: string[]
  status: InvitationStatus
  created_at: string
  expires_at: string
  accepted_at?: string
  accepted_by?: string
}

export interface InvitationConfig {
  email: string
  roles: string[]
  ttl_hours?: number
  account_id?: string
}

export interface PublicInvitation {
  id: string
  token: string
  account_id: string
  account_name: string
  invited_by_name: string
  email: string
  roles: string[]
  status: InvitationStatus
  expires_at: string
}

export interface InvitationAcceptRequest {
  email?: string
  password: string
  firstname?: string
  lastname?: string
}

export interface ServiceEndpoint {
  kind: string
  url: string
  health: Record<string, never>
  proxy_host?: string
  proxy_port?: number
}

export interface LoginResponse {
  user: User
  token: string
  activity: Activity
  service_endpoints: Record<string, ServiceEndpoint>
  is_super_admin: boolean
  is_new_user?: boolean
  invite_token?: string
}

export interface OAuthNewUserResponse {
  is_new_user: true
  email: string
  name: string
  picture: string
  oauth_subject: string
}

export interface OAuthPublicConfig {
  enabled: boolean
  google?: { client_id: string; redirect_uri: string }
  allow_self_service_accounts?: boolean
}

export interface UserDetail {
  user: User
  activity?: Activity
}

export interface AgentConfigurationOptions {
  account_id: string
  host_id: string
  hostname: string
}

export interface AgentConfiguration {
  account_id: string
  host_id: string
  jwt: string
  seed: string
  certificate: string
  private_key: string
  ca: string
  controller_url: string
}

export type APITokenType = 'cloudflare' | 'navionics' | 'thunderforest' | 'openstreetmap' | 'bearer' | 'custom'
export const API_TOKEN_TYPES: APITokenType[] = ['cloudflare', 'navionics', 'thunderforest', 'openstreetmap', 'bearer', 'custom']

// user: only the owner sees and uses it (GET/POST/PUT/DELETE /api/v1/apitokens).
// account: every user in the account sees and uses it; administrators manage
// it (/api/v1/accounts/me/apitokens). The scope is fixed by the route, not
// by the body.
export type APITokenScope = 'user' | 'account'
export const API_TOKEN_SCOPES: APITokenScope[] = ['user', 'account']

// Which resource a token authenticates against. Opaque to the platform.
export interface APITokenMetadata {
  resource_type?: string
  resource_id?: string
  resource_name?: string
  endpoint?: string
  tags?: Record<string, string>
}

// What the server returns. There is no secret field: the stored value is
// never sent back at any privilege level; has_token says whether one is set.
export interface APIToken {
  name: string
  type: APITokenType
  scope: APITokenScope
  id: string
  has_token: boolean
  metadata: APITokenMetadata
  user_id: string
  created_at: string
  updated_at: string
}

// Create body. On update every field is optional and an absent or empty
// token leaves the stored secret alone.
export interface APITokenInput {
  name: string
  type: APITokenType
  id?: string
  token?: string
  metadata?: APITokenMetadata
}

// How a stored API token yields the bearer the backend sends. static: the
// stored secret is the bearer. The OIDC modes store durable inputs (issuer,
// client ID, scopes, ... as metadata tags; the client secret, password, or
// refresh token sealed in the secret) and the backend mints short-lived
// tokens from them (admin.APITokenResolver in Go). See lib/apitokenAuth.ts
// for the tag names and the read/write helpers.
export type APITokenAuthMode = 'static' | 'client_credentials' | 'password' | 'refresh_token'
export const API_TOKEN_AUTH_MODES: APITokenAuthMode[] = ['static', 'client_credentials', 'password', 'refresh_token']

// Which token from the provider's response is sent as the bearer.
export type OIDCTokenField = 'access_token' | 'id_token'
export const OIDC_TOKEN_FIELDS: OIDCTokenField[] = ['access_token', 'id_token']

// How the client authenticates to the token endpoint. auto lets the backend
// choose from what discovery advertises; none is a public client.
export type OIDCClientAuthMethod = 'auto' | 'client_secret_basic' | 'client_secret_post' | 'none'
export const OIDC_CLIENT_AUTH_METHODS: OIDCClientAuthMethod[] = ['auto', 'client_secret_basic', 'client_secret_post', 'none']

export interface CertificateInfo {
  id: string
  subject: string
  issuer: string
  serial_number: string
  not_before: string
  not_after: string
  dns_names?: string[]
  ip_addresses?: string[]
  is_ca: boolean
  type: 'server' | 'ca' | 'intermediate' | 'agent' | 'letsencrypt' | string
  status: 'valid' | 'expiring' | 'expired' | string
  days_remaining: number
  file_path?: string
}

export interface PKIStatus {
  enabled: boolean
  lets_encrypt?: {
    enabled: boolean
    provider: string
    domains: string[]
    email: string
    challenge_type: string
    cert_cache_path: string
  }
  internal_pki?: {
    cert_file: string
    key_file: string
    ca_file: string
    has_ca: boolean
    has_cert: boolean
    has_key: boolean
  }
  certificates: CertificateInfo[]
}

export interface LogEntry {
  id: string
  timestamp: number
  service: string
  level: string
  message: string
  component: string
  /** OS hostname of the node that published the entry; absent from entries stored before it was recorded. */
  hostname?: string
  fields?: Record<string, unknown>
}

/** One group of GET /api/v1/logs/summary: a field value and how many matching entries carry it. */
export interface LogBucket {
  key: string
  count: number
}

export interface StoreMetrics {
  size_lsm: number
  size_vlog: number
  size_total: number
  num_keys: number
  num_levels: number
  num_tables: number
  level_sizes: { level: number; num_tables: number; size: number }[] | null
  block_cache_hits: number
  block_cache_misses: number
  disk_total: number
  disk_free: number
  disk_used_pct: number
  put_ops: number
  get_ops: number
  delete_ops: number
}

export interface StoreMetricsEntry {
  id: string
  timestamp: number
  hostname: string
  service: string
  store_name: string
  metrics: StoreMetrics
}

export interface NatsConnInfo {
  cid: number
  kind?: string
  type?: string
  ip: string
  port: number
  start: string
  last_activity: string
  rtt?: string
  uptime: string
  idle: string
  pending_bytes: number
  in_msgs: number
  out_msgs: number
  in_bytes: number
  out_bytes: number
  subscriptions: number
  name?: string
  lang?: string
  version?: string
  tls_version?: string
  authorized_user?: string
  account?: string
  subscriptions_list?: string[]
  name_tag?: string
}

export interface NatsConnz {
  server_id: string
  now: string
  num_connections: number
  total: number
  offset: number
  limit: number
  connections: NatsConnInfo[]
}

export interface NatsVarz {
  server_id: string
  server_name: string
  version: string
  go: string
  host: string
  port: number
  max_connections: number
  max_payload: number
  start: string
  now: string
  uptime: string
  mem: number
  cores: number
  cpu: number
  connections: number
  total_connections: number
  routes: number
  leafnodes: number
  in_msgs: number
  out_msgs: number
  in_bytes: number
  out_bytes: number
  slow_consumers: number
  subscriptions: number
  [key: string]: unknown
}

// Job queue (platform/job, served by platform/queue and proxied by the admin
// server at /api/v1/queue)
export type QueueJobState = 'waiting' | 'started' | 'complete' | 'cancelled' | 'error'

export interface QueueJob {
  id: string
  service: string
  name: string
  payload?: unknown
}

export interface QueueJobStatus {
  job_id: string
  service: string
  job_name: string
  timestamp: string
  state: QueueJobState | ''
  running: boolean
  result: string
  errors?: string[]
}

export interface QueueRecord {
  job: QueueJob
  status?: QueueJobStatus | null
}

// AI assistant (platform/ai, served by the admin server at /api/v1/ai).
// These mirror ai/message.go, ai/session.go, and the admin AI routes.
export interface AIUsage {
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
}

export interface AIToolCall {
  id: string
  name: string
  input: unknown
}

// ai.Result as the loop records it: the content handed to the model and
// whether the tool reported a failure.
export interface AIToolResultContent {
  content: string
  is_error?: boolean
}

export interface AIToolResult {
  id: string
  name: string
  result: AIToolResultContent
  // Set when the approval policy refused the call; result then carries
  // the message the model was given.
  denied?: boolean
}

export type AIRole = 'user' | 'assistant'

export interface AIMessage {
  role: AIRole
  // The provider's native content blocks, kept verbatim for the model.
  content: unknown
  // The user's prompt or the assistant's visible text.
  text?: string
  tool_calls?: AIToolCall[]
  tool_results?: AIToolResult[]
  created_at: string
}

export interface AISession {
  id: string
  account_id: string
  user_id: string
  title?: string
  provider: string
  model: string
  credential?: string
  messages: AIMessage[]
  usage: AIUsage
  created_at: string
  updated_at: string
}

export interface AISessionSummary {
  id: string
  title?: string
  provider: string
  model: string
  message_count: number
  usage: AIUsage
  created_at: string
  updated_at: string
}

export interface AISessionInput {
  title?: string
  // The vault name of the model credential; empty picks the first visible.
  credential?: string
}

export type AIEventType = 'text' | 'thinking' | 'tool_use' | 'tool_result' | 'usage' | 'compaction' | 'done' | 'error'

// One item of the stream a run emits; only the fields relevant to type
// are set.
export interface AIEvent {
  type: AIEventType
  // The delta for text and thinking, the summary for compaction.
  text?: string
  call?: AIToolCall
  result?: AIToolResult
  usage?: AIUsage
  // end_turn, max_tokens, refusal, max_turns, or a provider-specific reason.
  stop_reason?: string
  error?: string
}

export interface AICredentialRef {
  name: string
  provider: string
  model: string
}

export interface AIStatus {
  enabled: boolean
  // The credential a session gets when none is named.
  model: AICredentialRef | null
  // Every model credential the caller may name when creating a session,
  // the default first.
  models: AICredentialRef[]
  embedding: AICredentialRef | null
  tools: string[]
  mcp_servers: string[]
  allow_writes: boolean
}

export interface AIDocument {
  id: string
  content: string
  metadata?: Record<string, string>
}

export interface AISearchResult {
  id: string
  content: string
  metadata: Record<string, string>
  similarity: number
}

// The kinds of AI credential POST /api/v1/ai/credentials/test accepts.
export type AICredentialKind = 'ai_model' | 'ai_embedding' | 'mcp_server'

// One selectable assistant from GET /api/v1/ai/services: the admin's own
// ('admin'), a service assistant a product registered with the admin's
// gateway (its id is the service's), or 'all', the coordinator that asks
// the others (admin.AssistantServiceInfo).
export interface AssistantService {
  id: string
  name: string
}

// Why a credential test failed (aiCheckResult.error_kind in admin/ai_check.go).
export type AICredentialCheckErrorKind = 'auth' | 'not_found' | 'unreachable' | 'rejected' | 'unsupported'

// The answer of POST /api/v1/ai/credentials/test. A failed test is a 200
// with ok false; message is safe to show (it never carries the secret).
export interface AICredentialCheck {
  ok: boolean
  name: string
  kind: AICredentialKind
  latency_ms: number
  message: string
  error_kind?: AICredentialCheckErrorKind
  model?: string
  display_name?: string
  context_window?: number
  max_output?: number
  dimensions?: number
  tools?: number
}

// One model a provider's endpoint serves (ai.ModelInfo).
export interface AIModelOption {
  id: string
  display_name?: string
  context_window?: number
}

// The body of POST /api/v1/ai/credentials/models: the form's provider and
// endpoint, with either the key typed into it or the name of a saved
// credential whose stored key (and endpoint, when none is given) is used.
export interface AIModelsInput {
  provider: string
  endpoint?: string
  token?: string
  name?: string
}

// Its answer. A failure is a 200 with ok false; message never carries the
// secret.
export interface AIModelsResult {
  ok: boolean
  provider: string
  models: AIModelOption[]
  message?: string
  error_kind?: AICredentialCheckErrorKind
}

// AI usage (platform/ai/usage, collected by the admin server and served at
// /api/v1/ai/usage). These mirror ai.RunRecord (ai/usage_sink.go), and
// usage.Record, usage.Totals, and usage.Heartbeat (ai/usage/usage.go).

// One run as stored and reported. The ai.RunRecord fields are embedded in
// the Go struct, so they sit flat beside id/instance/host. duration is a Go
// time.Duration: integer nanoseconds.
export interface AIUsageRecord {
  id: string
  instance: string
  service?: string
  host: string
  account_id: string
  user_id: string
  session_id: string
  provider: string
  model: string
  credential?: string
  turns: number
  tool_calls: number
  usage: AIUsage
  stop_reason?: string
  error?: string
  started: string
  duration: number
  at: string
  source_account?: string
}

// Cumulative counters (usage.Totals).
export interface AIUsageTotals {
  runs: number
  turns: number
  tool_calls: number
  errors: number
  usage: AIUsage
}

// What an instance reports every heartbeat interval (usage.Heartbeat).
export interface AIUsageHeartbeat {
  instance: string
  service?: string
  host: string
  version?: string
  provider?: string
  model?: string
  started_at: string
  at: string
  interval_seconds: number
  totals: AIUsageTotals
  source_account?: string
}

// One running instance as the collector knows it (GET /api/v1/ai/usage/instances).
// id is "{instance}@{host}".
export interface AIInstanceSummary {
  id: string
  instance: string
  service?: string
  host: string
  version?: string
  provider?: string
  model?: string
  source_account?: string
  started_at: string
  last_seen: string
  online: boolean
  interval_seconds: number
  totals: AIUsageTotals
  last_run_at?: string
}

// What GET /api/v1/ai/usage/summary groups by.
export type AIUsageGroupBy = 'instance' | 'model' | 'account' | 'day'

// One row of GET /api/v1/ai/usage/summary.
export interface AIUsageGroup {
  key: string
  totals: AIUsageTotals
}

// The answer of GET /api/v1/ai/usage/runs: one page, newest first, and the
// number of records that match the filters.
export interface AIUsageRunsPage {
  records: AIUsageRecord[]
  total: number
}

// The filters and page of GET /api/v1/ai/usage/runs.
export interface AIUsageRunsQuery {
  instance?: string
  host?: string
  account_id?: string
  model?: string
  limit?: number
  offset?: number
}
