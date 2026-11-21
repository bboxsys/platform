# Renewable OIDC API tokens

How a backend built on `github.com/bboxsys/platform` stores durable OIDC
inputs for a third-party API and mints the short-lived bearer each call
needs, without a secret or a minted token ever reaching the browser. This
document covers the storage contract, the Go resolver, the `@bboxsys/ui`
form and helpers, and what a product adds on top. The general API token
vault (routes, scopes, sealing) is in
[DEVELOPER.md, API tokens](DEVELOPER.md#api-tokens-bring-your-own-credential-vault).

## Contents

1. [Why](#1-why)
2. [Credential modes](#2-credential-modes)
3. [Storage contract](#3-storage-contract)
4. [Backend: admin.APITokenResolver](#4-backend-adminapitokenresolver)
5. [Package oidctoken](#5-package-oidctoken)
6. [Frontend: @bboxsys/ui](#6-frontend-bboxsysui)
7. [Adding a product credential](#7-adding-a-product-credential)
8. [Provisioning boundary](#8-provisioning-boundary)
9. [Testing](#9-testing)
10. [Standards](#10-standards)

---

## 1. Why

An API that authenticates with OIDC issues access tokens that last an hour
or less. A stored bearer therefore stops working within the hour, while the
credential that can mint another one (an OAuth client secret, a refresh
token, or for legacy providers an automation user's password) lasts months.
The platform stores the durable credential sealed in the API token vault
and mints on demand in the backend:

1. read the issuer's `/.well-known/openid-configuration`,
2. check that the returned issuer matches the configured one,
3. use the advertised token endpoint and client authentication methods,
4. request a token with one of the standard grants,
5. cache it until shortly before `expires_in` (or the JWT `exp` claim) and
   then mint again.

Nothing in the platform knows about a particular identity provider or a
particular resource server. Duo, Keycloak, Okta, and Entra are all just an
issuer URL.

## 2. Credential modes

| Mode (`oidc_auth_mode`) | Durable inputs | Intended use |
|---|---|---|
| `static` (or no tag) | the bearer | an already-minted token; compatibility |
| `client_credentials` | issuer, client ID, client secret, scopes | service and machine-to-machine clients |
| `password` | issuer, client ID, optional client secret, username, password, scopes | legacy providers that require the password grant |
| `refresh_token` | issuer, client ID, optional client secret, refresh token, scopes | providers that issue renewable refresh tokens |

Two further settings apply to every OIDC mode:

- `oidc_token_field`: `access_token` (default) or `id_token`, for a
  resource server that specifically requires the ID token.
- `oidc_client_auth_method`: `auto` (default; choose from what discovery
  advertises), `client_secret_basic`, `client_secret_post`, or `none` for a
  public client.

The password grant is retained for compatibility only. Prefer client
credentials for a workload identity, or a refresh token obtained through an
interactive flow.

## 3. Storage contract

An OIDC credential is an ordinary `admin.APIToken` (type `bearer`, user or
account scope) whose fields are used as follows. The Go constants are in
`admin/apitoken_oidc.go`; `@bboxsys/ui` mirrors them in
`src/lib/apitokenAuth.ts`. The two must stay in step.

| Where | Field or tag | Content |
|---|---|---|
| `ID` | | the OAuth client ID |
| `Token` (sealed) | | `{"version":1,"client_secret":"...","password":"...","refresh_token":"..."}` with only the keys the mode needs (`APITokenOIDCSecret`) |
| `Metadata.Tags` | `oidc_auth_mode` | the mode; absent or `static` means `Token` is the bearer |
| | `oidc_issuer` | issuer URL, https only, no trailing slash |
| | `oidc_scopes` | space-separated scope parameter |
| | `oidc_username` | resource owner, password grant only |
| | `oidc_token_field` | `access_token` or `id_token` |
| | `oidc_client_auth_method` | `auto`, `client_secret_basic`, `client_secret_post`, `none` |
| | `oidc_insecure_skip_verify` | `true` to skip TLS verification for the identity provider ONLY |
| `Metadata.ResourceType/ID/Name/Endpoint` | | the product's: which API this opens and where |

Consequences of that layout:

- The vault's read API (`APITokenView`) shows the mode, issuer, and client
  ID, and `has_token`, but never the secret set or a minted bearer.
- The secrets travel together. Entering any one of them on an update
  replaces the whole sealed document, so a form must collect every secret
  the mode needs when rotating one. Leaving all of them blank keeps the
  stored set (the vault's usual "absent token keeps the secret" rule).
- Short-lived access and ID tokens exist only in the backend's memory
  cache. A rotated refresh token is the one thing written back, sealed,
  before it is needed again.
- The identity provider's TLS flag is separate from any flag the product
  keeps for the resource server (a product may keep its own
  `insecure_skip_verify` for the API it calls), because the two often use
  different certificate authorities.

## 4. Backend: admin.APITokenResolver

Only the process that hosts the admin server holds the store and can open a
sealed secret, so the resolver lives in the root `admin` package and is
constructed once per process:

```go
resolver := admin.NewAPITokenResolver(adminServer.Store())

token, err := store.GetAPITokenVisibleTo(accountID, userID, name) // user's own, then account
if err != nil { ... }
if token.Metadata.ResourceType != "my_product_api" {              // the product's gate
    return ErrNotFound
}
bearer, err := resolver.Bearer(ctx, accountID, token)
```

`Bearer` returns a static token's `Token` as stored. For an OIDC token it
calls `token.OIDCSource()` (tags + sealed secret to an `oidctoken.Source`,
validated), asks the shared `oidctoken.Manager` for a token, and if the
provider rotated the refresh token it seals the replacement with
`PutAPIToken` before returning. Two credentials with identical OIDC inputs
(for example one per hostname of the same deployment) share one cached
token because the cache key is the whole source.

`resolver.Forget(token)` drops the cached bearer so the next call mints
again; use it when the resource server rejects a token that has not
expired (a revoked session, a rotated client secret). Building blocks for
code that needs them directly: `token.AuthMode()`, `token.OIDCSource()`,
`admin.DecodeAPITokenOIDCSecret`, `APITokenOIDCSecret.Encode`, and
`token.Metadata.Flag(tag)` for boolean tags.

Hand the resolver, or an interface it satisfies, to the service packages
that make outbound calls. Service packages import only `admin/client`, so
they declare the interface and the host process satisfies it.

## 5. Package oidctoken

`github.com/bboxsys/platform/oidctoken` is the token exchange without the
store. It imports nothing from `admin`, so a service that holds its own
OIDC client inputs can use it directly.

```go
type Source struct {
    Grant              Grant            // GrantClientCredentials, GrantPassword, GrantRefreshToken
    Issuer, ClientID   string
    ClientSecret       string
    Username, Password string           // password grant
    RefreshToken       string           // refresh grant
    Scopes             string           // space-separated
    TokenField         TokenField       // TokenFieldAccess (default), TokenFieldID
    ClientAuthMethod   ClientAuthMethod // ClientAuthAuto (default), SecretBasic, SecretPost, None
    InsecureSkipVerify bool             // identity provider only
}
func (s Source) Validate() error

type Token struct { Value, RefreshToken string; ExpiresAt time.Time }
func Mint(ctx context.Context, source Source) (Token, error)   // discovery + grant, no cache
func Expiration(token string, expiresIn json.RawMessage, now time.Time) time.Time

type Manager
func NewManager() *Manager
func (m *Manager) Token(ctx context.Context, source Source) (Token, error)
func (m *Manager) Forget(source Source)
```

Behaviour worth knowing:

- Issuer and token endpoint must be `https` with a host and no embedded
  credentials; query and fragment are dropped. Redirects are not followed,
  so a misconfigured issuer cannot move the client secret to another host.
- Discovery's `issuer` must equal the configured issuer (trailing slash
  ignored). If discovery lists `grant_types_supported`, the grant must be in
  it; if it lists `token_endpoint_auth_methods_supported`, the chosen
  method must be in it.
- `auto` picks `none` without a client secret, else `client_secret_basic`
  when advertised or when nothing is advertised, else `client_secret_post`.
- `Manager` renews 60 seconds before expiry, or a fifth of the lifetime
  for tokens shorter than two minutes; a token with no discoverable expiry
  is returned but not cached. Concurrent requests for one source share a
  single mint. When a mint returns a rotated refresh token the entry is
  also cached under the rotated source, so the caller that persists the
  rotation does not mint twice.
- Requests time out after 20 seconds and read at most 1 MiB.

## 6. Frontend: @bboxsys/ui

The kit owns the browser half of the contract so no app forks the OIDC
form. Everything is exported from the package root.

```ts
import {
  ApiTokenAuthFields,                       // the form section
  emptyAPITokenAuthForm, apiTokenAuthForm,  // form state for create / edit
  readAPITokenAuth,                         // APIToken -> APITokenAuth (settings, no secrets)
  withAPITokenAuth,                         // apply auth to an APITokenInput body
  validateAPITokenAuth,                     // string | null, before writing
  API_TOKEN_AUTH_MODE_LABELS,
} from '@bboxsys/ui'
import type { APITokenAuth, APITokenAuthForm, APITokenAuthInput } from '@bboxsys/ui'
```

- `APITokenAuth` is what the server shows: `authMode`, `oidcIssuer`,
  `oidcClientId`, `oidcUsername`, `oidcScopes`, `oidcTokenField`,
  `oidcClientAuthMethod`, `oidcInsecureSkipVerify`. A product credential
  type extends it with its own fields.
- `APITokenAuthInput` adds the one-way secrets: `token`,
  `oidcClientSecret`, `oidcPassword`, `oidcRefreshToken`.
- `withAPITokenAuth(body, auth)` sets `id` to the client ID, merges the
  `oidc_*` tags into `body.metadata.tags` (keeping the product's tags), and
  sets `token` only when a secret was entered, so a blank edit keeps the
  stored secret.
- `validateAPITokenAuth(form, previous)` encodes the rules of section 3:
  a new credential, a mode change, or a change of issuer or client ID needs
  the full secret set; an unchanged edit may leave them blank; entering any
  OIDC secret requires the others the mode needs.
- `ApiTokenAuthFields` renders the mode select, the bearer field for
  `static`, and otherwise issuer, client ID and secret, username and
  password or refresh token as the grant needs, scopes, token field, token
  endpoint authentication, and the provider TLS checkbox. Props: `editing`,
  `audience` ("Token sent to the controller"), `modes`, `idPrefix`,
  `disabled`. It binds an `APITokenAuthForm` with `v-model` and does not
  validate.

## 7. Adding a product credential

What a product credentials page supplies:

1. A resource type per API it opens (`example_intent`,
   `example_intent_grpc`), the endpoint, and a resource association,
   written with `withAPITokenAuth` around its own `APITokenInput`.
2. Any product tags of its own, such as a TLS flag for the resource server.
3. A backend gate on the resource type before calling `resolver.Bearer`,
   so the route cannot become a general-purpose outbound proxy.
4. A liveness probe, because whether a credential still works is a product
   question (which cheap call exercises the bearer and its role).
5. Optional conveniences: discovery of issuer and client ID from the
   product's own inventory, default scopes, the mode a new credential
   starts in (`emptyAPITokenAuthForm({ authMode: 'client_credentials',
   oidcScopes: '...' })`).

The generic vault page hides credentials a product section claims by
resource type, so a credential appears once.

## 8. Provisioning boundary

OIDC discovery and OAuth token grants provide no portable way to create or
rotate an OAuth client from an administrator account. Dynamic client
registration (RFC 7591) is optional and needs a separate initial access
credential. Creating a Duo OAuth application or a Keycloak client therefore
stays a provisioning step outside the resolver: the platform stores the
resulting client ID and secret and renews access tokens from them. It does
not store an identity-provider administrator credential or automate
provider-specific application creation. If that is ever needed it should
be an explicit provider adapter with narrowly scoped administrative
credentials, not part of the token exchange.

## 9. Testing

`oidctoken/oidctokentest.NewIssuer(t, authMethods...)` starts a TLS fake
provider serving discovery and a token endpoint; sources pointed at it need
`InsecureSkipVerify: true`. It records the last token request (`Form()`,
`BasicAuth()`), counts calls (`TokenCalls()`), and `SetResponse(key, value)`
edits what the token endpoint returns (for example a rotated
`refresh_token`). The platform's own tests in `oidctoken` and
`admin/apitoken_oidc_test.go` show the patterns; a product resolver test can use
the same issuer.

Frontend: `ui/src/lib/apitokenAuth.spec.ts` and
`ui/src/components/ApiTokenAuthFields.spec.ts` (vitest, happy-dom).

## 10. Standards

- [OpenID Connect Discovery 1.0](https://openid.net/specs/openid-connect-discovery-1_0.html)
- [OAuth 2.0 Authorization Framework](https://www.rfc-editor.org/rfc/rfc6749.html)
- [OAuth 2.0 Authorization Server Metadata](https://www.rfc-editor.org/rfc/rfc8414.html)
- [OAuth 2.0 Dynamic Client Registration](https://www.rfc-editor.org/rfc/rfc7591.html)
- [OAuth 2.0 Security Best Current Practice](https://www.rfc-editor.org/rfc/rfc9700.html)
