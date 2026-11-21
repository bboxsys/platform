import type {
  APIToken, APITokenAuthMode, APITokenInput, OIDCClientAuthMethod, OIDCTokenField,
} from '../types/admin'

// The authentication of a stored API token, as the platform admin server
// records it. This is the wire contract with admin.APITokenResolver in Go
// (bboxsys/platform admin/apitoken_oidc.go): the non-secret OIDC settings
// are metadata tags, the client ID is the token's id, and the client secret,
// password, and refresh token travel together as one JSON secret. A product
// section that stores credentials of its own kind composes these helpers
// with its own resource type, endpoint, and extra tags.

export const API_TOKEN_AUTH_MODE_TAG = 'oidc_auth_mode'
export const API_TOKEN_OIDC_ISSUER_TAG = 'oidc_issuer'
export const API_TOKEN_OIDC_SCOPES_TAG = 'oidc_scopes'
export const API_TOKEN_OIDC_USERNAME_TAG = 'oidc_username'
export const API_TOKEN_OIDC_TOKEN_FIELD_TAG = 'oidc_token_field'
export const API_TOKEN_OIDC_CLIENT_AUTH_METHOD_TAG = 'oidc_client_auth_method'
export const API_TOKEN_OIDC_INSECURE_SKIP_VERIFY_TAG = 'oidc_insecure_skip_verify'

export const API_TOKEN_AUTH_MODE_LABELS: Record<APITokenAuthMode, string> = {
  static: 'Static bearer token',
  client_credentials: 'OIDC client credentials',
  password: 'OIDC username and password',
  refresh_token: 'OIDC refresh token',
}

export const OIDC_TOKEN_FIELD_LABELS: Record<OIDCTokenField, string> = {
  access_token: 'Access token',
  id_token: 'ID token',
}

export const OIDC_CLIENT_AUTH_METHOD_LABELS: Record<OIDCClientAuthMethod, string> = {
  auto: 'Auto-discover',
  client_secret_basic: 'Client secret basic',
  client_secret_post: 'Client secret post',
  none: 'None (public client)',
}

// The non-secret authentication settings of a stored token: what the read
// API shows. A static token has authMode 'static' and empty OIDC fields.
export interface APITokenAuth {
  authMode: APITokenAuthMode
  oidcIssuer: string
  oidcClientId: string
  oidcUsername: string
  oidcScopes: string
  oidcTokenField: OIDCTokenField
  oidcClientAuthMethod: OIDCClientAuthMethod
  oidcInsecureSkipVerify: boolean
}

// The write shape: the settings plus the secrets, which travel one way. All
// are optional so a form can leave them blank to keep the stored ones.
export interface APITokenAuthInput extends Partial<APITokenAuth> {
  token?: string
  oidcClientSecret?: string
  oidcPassword?: string
  oidcRefreshToken?: string
}

// The state an authentication form binds to (ApiTokenAuthFields): every
// setting and every secret, all present, secrets always starting blank.
export interface APITokenAuthForm extends APITokenAuth {
  token: string
  oidcClientSecret: string
  oidcPassword: string
  oidcRefreshToken: string
}

export const DEFAULT_OIDC_SCOPES = 'openid email profile'

// A blank form. Pass overrides for a product's defaults, such as the mode a
// new credential should start in or the scopes its API expects.
export function emptyAPITokenAuthForm(overrides: Partial<APITokenAuthForm> = {}): APITokenAuthForm {
  return {
    authMode:               'static',
    token:                  '',
    oidcIssuer:             '',
    oidcClientId:           '',
    oidcClientSecret:       '',
    oidcUsername:           '',
    oidcPassword:           '',
    oidcRefreshToken:       '',
    oidcScopes:             DEFAULT_OIDC_SCOPES,
    oidcTokenField:         'access_token',
    oidcClientAuthMethod:   'auto',
    oidcInsecureSkipVerify: false,
    ...overrides,
  }
}

// The form for editing a stored token: its settings, with the secrets blank
// because nothing can read them back.
export function apiTokenAuthForm(auth: APITokenAuth): APITokenAuthForm {
  return { ...emptyAPITokenAuthForm(), ...auth, token: '', oidcClientSecret: '', oidcPassword: '', oidcRefreshToken: '' }
}

// Reads the authentication settings off a token the server returned. A
// token written before modes existed has no tag and is static.
export function readAPITokenAuth(token: APIToken): APITokenAuth {
  const tags = token.metadata?.tags ?? {}
  const authMode = (tags[API_TOKEN_AUTH_MODE_TAG] || 'static') as APITokenAuthMode
  return {
    authMode,
    oidcIssuer:             tags[API_TOKEN_OIDC_ISSUER_TAG] ?? '',
    oidcClientId:           authMode === 'static' ? '' : token.id ?? '',
    oidcUsername:           tags[API_TOKEN_OIDC_USERNAME_TAG] ?? '',
    oidcScopes:             tags[API_TOKEN_OIDC_SCOPES_TAG] ?? '',
    oidcTokenField:         (tags[API_TOKEN_OIDC_TOKEN_FIELD_TAG] || 'access_token') as OIDCTokenField,
    oidcClientAuthMethod:   (tags[API_TOKEN_OIDC_CLIENT_AUTH_METHOD_TAG] || 'auto') as OIDCClientAuthMethod,
    oidcInsecureSkipVerify: tags[API_TOKEN_OIDC_INSECURE_SKIP_VERIFY_TAG] === 'true',
  }
}

// True when the input carries any OIDC secret, which replaces the whole
// sealed secret set.
export function hasOIDCSecret(auth: APITokenAuthInput): boolean {
  return !!(auth.oidcClientSecret || auth.oidcPassword || auth.oidcRefreshToken)
}

// The metadata tags that record the authentication. Sent whole on every
// write, so clearing a setting clears the stored one.
export function apiTokenAuthTags(auth: APITokenAuthInput): Record<string, string> {
  const authMode = auth.authMode ?? 'static'
  const tags: Record<string, string> = { [API_TOKEN_AUTH_MODE_TAG]: authMode }
  if (authMode === 'static') return tags
  tags[API_TOKEN_OIDC_ISSUER_TAG] = normalizeIssuer(auth.oidcIssuer ?? '')
  tags[API_TOKEN_OIDC_SCOPES_TAG] = (auth.oidcScopes ?? '').trim()
  tags[API_TOKEN_OIDC_USERNAME_TAG] = (auth.oidcUsername ?? '').trim()
  tags[API_TOKEN_OIDC_TOKEN_FIELD_TAG] = auth.oidcTokenField ?? 'access_token'
  tags[API_TOKEN_OIDC_CLIENT_AUTH_METHOD_TAG] = auth.oidcClientAuthMethod ?? 'auto'
  if (auth.oidcInsecureSkipVerify) tags[API_TOKEN_OIDC_INSECURE_SKIP_VERIFY_TAG] = 'true'
  return tags
}

// The secret to send, or undefined to keep the stored one. A static token's
// secret is the bearer; an OIDC token's is the JSON secret set.
export function apiTokenAuthSecret(auth: APITokenAuthInput): string | undefined {
  if ((auth.authMode ?? 'static') === 'static') return auth.token || undefined
  if (!hasOIDCSecret(auth)) return undefined
  return JSON.stringify({
    version: 1,
    ...(auth.oidcClientSecret ? { client_secret: auth.oidcClientSecret } : {}),
    ...(auth.oidcPassword ? { password: auth.oidcPassword } : {}),
    ...(auth.oidcRefreshToken ? { refresh_token: auth.oidcRefreshToken } : {}),
  })
}

// Applies the authentication to a write body: sets id (the OIDC client ID),
// merges the tags into metadata, and sets token when a secret was entered.
// Tags the caller already put in metadata are kept.
export function withAPITokenAuth<T extends Partial<APITokenInput>>(
  body: T, auth: APITokenAuthInput,
): T & Partial<APITokenInput> {
  const authMode = auth.authMode ?? 'static'
  const out: T & Partial<APITokenInput> = {
    ...body,
    id: authMode === 'static' ? body.id ?? '' : (auth.oidcClientId ?? '').trim(),
    metadata: { ...body.metadata, tags: { ...body.metadata?.tags, ...apiTokenAuthTags(auth) } },
  }
  const secret = apiTokenAuthSecret(auth)
  if (secret !== undefined) out.token = secret
  else delete out.token
  return out
}

export function normalizeIssuer(issuer: string): string {
  return issuer.trim().replace(/\/$/, '')
}

// Validates a form before it is sent. `previous` is the stored auth when
// editing (null when creating). Returns the message to show, or null.
//
// Secrets are the subtle part. A new credential, a mode change, or a change
// of OIDC identity (issuer or client ID) needs the full secret set for the
// mode; an edit that keeps them may leave every secret blank. Entering any
// OIDC secret replaces the sealed set, so the others the mode needs must
// come with it.
export function validateAPITokenAuth(form: APITokenAuthForm, previous: APITokenAuth | null): string | null {
  const oidc = form.authMode !== 'static'
  const fresh = previous === null
  const modeChanged = !!previous && previous.authMode !== form.authMode
  const identityChanged = !!previous && oidc && (
    previous.oidcIssuer !== normalizeIssuer(form.oidcIssuer) || previous.oidcClientId !== form.oidcClientId.trim()
  )
  const replacingSecret = fresh || modeChanged || identityChanged
  const entered = hasOIDCSecret(form)
  if (oidc && (!normalizeIssuer(form.oidcIssuer) || !form.oidcClientId.trim())) {
    return 'OIDC issuer and client ID are required.'
  }
  if (oidc && !/^https:\/\/\S+$/.test(normalizeIssuer(form.oidcIssuer))) {
    return 'The OIDC issuer must be an https URL.'
  }
  if ((replacingSecret || entered) && form.authMode === 'client_credentials' && !form.oidcClientSecret) {
    return 'Client credentials require a client secret.'
  }
  if ((replacingSecret || entered) && form.authMode === 'password' && (!form.oidcUsername.trim() || !form.oidcPassword)) {
    return 'The password grant requires a username and password.'
  }
  if ((replacingSecret || entered) && form.authMode === 'refresh_token' && !form.oidcRefreshToken) {
    return 'The refresh-token grant requires a refresh token.'
  }
  if ((fresh || modeChanged) && form.authMode === 'static' && !form.token) {
    return 'A bearer token is required.'
  }
  if (entered && form.oidcClientAuthMethod !== 'none' && !form.oidcClientSecret) {
    return 'Re-enter the client secret when replacing the sealed OIDC secret set, or select a public client.'
  }
  return null
}
