# platform Developer Reference

The API contract and integration recipes for `github.com/bboxsys/platform`
(go 1.26). A typical product imports `admin/client`, `server`, `config`,
`logger`, `messenger`, `job`, `worker`, `queue`, `supervisor`, `controller`,
`host`, `pki`, `storemetrics`, `logentry`, `letsencrypt`, and root `admin`
only from the command that hosts the admin server; a process that only
serves a SPA needs just `server` for SPAHandler.

Contents:
1. [Authenticating a service (admin/client)](#1-authenticating-a-service-adminclient)
2. [The base HTTP server (server)](#2-the-base-http-server-server)
3. [Hosting the admin server (admin)](#3-hosting-the-admin-server-admin)
4. [Identity model (user)](#4-identity-model-user)
5. [Process composition (supervisor, controller, host)](#5-process-composition)
6. [Config (config)](#6-config)
7. [Logging and telemetry (logger, logentry, storemetrics)](#7-logging-and-telemetry)
8. [NATS client (messenger)](#8-nats-client-messenger)
9. [Asynchronous jobs (job, worker, queue)](#9-asynchronous-jobs)
10. [PKI and Let's Encrypt (pki, letsencrypt)](#10-pki-and-lets-encrypt)
11. [SPA dev servers (@bboxsys/ui/vite)](#11-spa-dev-servers)
12. [The API over NATS (natsapi)](#12-the-api-over-nats)
13. [AI assistant (ai)](#13-ai-assistant-ai)
14. [Embedded documentation (docsite, ai/docsearch)](#14-embedded-documentation-docsite-aidocsearch)
15. [Gotchas](#15-gotchas)

---

## 1. Authenticating a service (admin/client)

Import as `admin "github.com/bboxsys/platform/admin/client"`. Bearer tokens
are NATS user JWTs signed by an account nkey; `Auth` verifies the signature
and that the issuing account is trusted -- entirely local, no callback to
the admin server.

**Wiring (the canonical recipe):**

```go
id := admin.NewID(admin.IDConfig{Logger: s.Logger, BasicAuth: false})
s.Router.Use(id.Middleware)                       // MUST run before auth
s.auth = admin.NewAuth(admin.AuthConfig{
    Logger:          s.Logger,
    BasicAuth:       false,
    TrustedAccounts: s.TrustedAccounts,           // account public keys from admin
})

// routes
s.Router.HandleFunc("/api/v1/things", s.auth.Bearer(s.GetThings)).Methods("GET")
s.Router.HandleFunc("/api/v1/admin/x", s.auth.RequireAdmin(s.adminAccountID, s.AdminX)).Methods("POST")

// handler
accountID := admin.AccountIDFromContext(r.Context())
userID := admin.UserIDFromContext(r.Context())    // NATS public key after Bearer
```

When the process also hosts the admin server, inject the store so tenant
admins (not only the super admin) pass role gates, and learn the admin
account:

```go
mediaServer.SetAdminAccountID(adminServer.AdminAccountID())
mediaServer.SetAuthStore(adminServer.Store())      // calls auth.SetStore
```

**ID middleware resolution** (`NewID(IDConfig{BasicAuth, Store, Logger})`,
`(*ID).Middleware`): accountID from header `X-Blackbox-AccountID` -> mux
var `{accountID}` -> query `?accountID=`; the accountID IS the account
public key. userID from header/var/query, else decoded from the
Authorization credential (Bearer: token subject; Basic with
`BasicAuth:true`: store lookup by email, which can also fill accountID).

**Auth** (`NewAuth(AuthConfig{BasicAuth, Store, Logger, TrustedAccounts})`):

```go
func (auth *Auth) Basic(h http.HandlerFunc) http.HandlerFunc
func (auth *Auth) Bearer(h http.HandlerFunc) http.HandlerFunc
func (auth *Auth) BasicOrBearer(h http.HandlerFunc) http.HandlerFunc
func (auth *Auth) RequireSuperAdmin(adminAccountID string, h http.HandlerFunc) http.HandlerFunc
func (auth *Auth) RequireRole(adminAccountID, role string, h http.HandlerFunc) http.HandlerFunc
func (auth *Auth) RequireAdmin(adminAccountID string, h http.HandlerFunc) http.HandlerFunc  // == RequireRole(..., user.RoleAdministrator, ...)
func (auth *Auth) AddTrustedAccount(id string)
func (auth *Auth) SetStore(st IdentityStore)
func (auth *Auth) IsPublicKeyTrusted(publicKey string) bool
```

- `Bearer`: 401 unless the context account key is trusted, then
  `user.ValidateUserToken(credential, publicKey)`; on success overwrites
  the context userID with `claims.Subject`.
- `RequireRole`: super-admin account short-circuits true; otherwise needs
  the store (`GetUserByPublicKey` + `user.HasRole`); no store => 403.
- Basic paths do lockout accounting: 5 failed logins lock the account
  (403 `{"error":"account locked"}` until unlocked via admin).

**Context accessors**: `AccountIDFromContext`, `UserIDFromContext`,
`AccountPublicKeyFromContext`, `ValidTokenFromContext`,
`ClaimsFromContext`, `NoAuthFromContext`, `ValidBasicAuthFromContext`.

**IdentityStore** (what Basic/RequireRole need; `*admin.Store` satisfies it):

```go
type IdentityStore interface {
    GetUserByEmail(email string) (*user.User, string, error)
    GetUserByPublicKey(accountID, publicKey string) (*user.User, error)
    GetActivity(accountID, activityID string) (user.Activity, error)
    UpdateActivity(accountID string, a user.Activity) error
    PutActivity(accountID string, activity user.Activity) error
    ValidateUserPassword(accountID, userID, password string) (bool, error)
}
```

**Service endpoints**: `ServiceEndpoint{Kind, URL, ProxyHost, ProxyPort}`,
`NewServiceEndpoint(kind, url)`, `ServiceEndpoints.AddEndpoint` (errors on
unknown kind), `EndpointKinds()`. Valid kinds: login_service,
admin_service, proxy_service, messenger_service, blackbox_service,
media_service, queue_service, controller_service, host_service,
comms_service, proxy. The SPA bootstraps from the unauthenticated
`/serviceendpoints/` route.

**Tenant lifecycle over NATS** (`admin/client/messages.go`):
`SubjectTenantCreated/Updated/Deleted` = `tenant.notify.created|updated|deleted`
with `TenantCreatedNotification{AccountID, AccountName, Timestamp}`
(Updated adds `PreviousName`; Deleted carries the last-known name).
Services subscribe to add trusted accounts / provision per-tenant
resources at runtime; e.g. a media service mkdirs the tenant
media directory and calls `AddTrustedAccount` on create.

---

## 2. The base HTTP server (server)

Every HTTP service embeds `*server.Server`: mux router with client-IP +
request-logging + permissive CORS middleware preinstalled, TLS from static
PEMs or Let's Encrypt, optional NATS client.

```go
type Server struct {
    ServerOptions   *ServerOptions
    Srv             *http.Server
    Logger          *logger.Logger
    Router          *mux.Router
    Websocket       *Websocket
    MessengerClient *messenger.Client
    TrustedAccounts []string
    LetsEncryptMgr  *letsencrypt.Manager
}
func NewServer(options *ServerOptions) (*Server, error)
func (s *Server) AddMessenger(options *messenger.ClientOptions) error
func (s *Server) Serve()                       // blocking; starts LE auto-renew if it owns the mgr
func (s *Server) Stop()
func (s *Server) ServeOpenAPI(spec []byte)     // GET /api/v1/openapi.json, unauthenticated
func SPAHandler(fsys fs.FS) http.Handler
```

`ServerOptions` (the one struct every service builds): `Application`,
`Component`, static TLS (`PrivateKey/Certificate/CA` PEM bytes), OR
`LetsEncrypt *config.LetsEncryptConfig` (server-owned manager) OR
`LetsEncryptManager *letsencrypt.Manager` (shared -- preferred in the
all-in-one binary; owner handles renewal), `HTTPPort`, `Messenger` +
`MessengerServerURL/ServerName/JWT/Seed/AccountID`, `TrustedAccounts`,
`BasicAuth`, and the untyped `Metadata map[string]interface{}` extension
point. CORS allows headers `X-Requested-With, content-type, authorization,
X-Blackbox-AccountID, X-Filename, X-Last-Modified`.

`SPAHandler` contract: GET/HEAD only; serves precompressed `.br`/`.gz`
variants by Accept-Encoding; `Cache-Control: public, max-age=31536000,
immutable` for `assets/**`, `no-cache` otherwise; refuses direct
`.gz`/`.br` requests; missing paths with asset extensions
(`.js .css .map .woff2 .png ...`) get 404 instead of index.html (stale
chunk hashes must fail loudly, not execute HTML); everything else falls
back to index.html for client-side routing. Every SPA serves through this -- pair it with a build that emits
`.br`/`.gz`.

Serving an OpenAPI spec: embed the JSON and call
`srv.ServeOpenAPI(openapiSpec)`. Every service should expose one; the shared
ApiExplorer consumes them.

---

## 3. Hosting the admin server (admin)

Only the hosting process imports the root package. Reference
implementation: `cmd/admin/main.go` (standalone).

```go
adminServer, err := admin.NewAdminServer(&server.ServerOptions{
    Application: appName, Component: "admin_server",
    PrivateKey: keyPEM, Certificate: certPEM, CA: caPEM,   // or LetsEncryptManager
    HTTPPort: adminPort,
    Metadata: map[string]interface{}{
        // REQUIRED (panic if missing): storeLocation (string), tokenValidMinutes (int)
        "storeLocation":     rootPath + "/admin.db",
        "tokenValidMinutes": 60,
        // secrets: secretsKeyFile (string) overrides the AES key file that
        //          seals stored API token secrets; default is secrets.key
        //          beside the store directory, generated 0600 on first run
        // first-run init: accountServerURL, operatorServiceURL,
        //                 defaultAdminEmail, defaultAdminPassword
        // PKI: intermediate_ca_file, intermediate_cert_file, intermediate_key_file
        // OAuth: oauth_enabled, google_client_id, google_client_secret,
        //        oauth_redirect_uri, oauth_state_ttl_minutes,
        //        allow_self_service_accounts
        // invites/email: invitation_default_ttl_hours, smtp_host, smtp_port,
        //        smtp_username, smtp_password, smtp_from, smtp_from_name,
        //        invite_accept_url, app_display_name
        // console branding (GET /branding/): branding_name, branding_logo_url,
        //        branding_brand ("blue" default, "orange": a @bboxsys/ui preset
        //        the SPA applies over its stylesheet), branding_css (the host's
        //        own :root / .dark brand token block when it is not a preset;
        //        no fonts by relative URL). Buttons, links, focus rings and the
        //        PageTabs underline follow it; the SPA caches the last brand
        //        per browser so a reload does not flash the default. Write
        //        them with admin.HostBranding{Name, LogoSVG, Brand, UI}.Metadata():
        //        UI is the host's embedded SPA, whose dist/brand-tokens.css
        //        (emitted by @bboxsys/ui/vite's brandTokens() from the SPA's
        //        src/brand.css) becomes branding_css, so the brand is written
        //        once in the SPA and never mirrored in Go.
    },
})
adminServer.PutServiceEndpointsMap(serviceEndpoints)
if uiFS := admin.UIFS(); uiFS != nil { adminServer.ServeUI(uiFS) }  // BEFORE Start(); needs -tags adminui
adminServer.InitLogStore(rootPath+"/logs.db", 100000)
adminServer.InitMetricsStore(rootPath+"/metrics.db", 100000)
adminServer.SetMonitoringPort(natsMonitoringPort)
adminServer.StartNATSAPI(admin.NATSAPIOptions{})  // admin_service over NATS on every collector connection (section 12)
go adminServer.Start()          // AddHandlers + Serve; poll Ready()
// ... on shutdown:
adminServer.Close()
```

First boot creates operator, the ADMIN account, the default admin user
(role `administrator`), and the SYS account, persisting
`InitializerData{OperatorID, AdminAccountID}`. Super-admin == membership
in the ADMIN account.

Host-process integration methods you will actually call:
`Store()`, `GetBaseStore()`, `AdminAccountID()`, `OperatorID()`,
`GetSysAccountID()`, `GetOperatorJWT()` (both feed the NATS controller),
`AddTrustedAccount(id)`,
`ServiceCredentials(accountName, serviceName) (*user.Credentials, error)`
(get-or-create NATS credentials for a service -- the bootstrap path for
messenger wiring), `AgentConfiguration(...)`, `LogStore()`,
`MetricsStore()`, `SetConfigPersister(fn)`.

Route auth tiers (as registered in AddHandlers): unauthenticated --
openapi.json, `/serviceendpoints/`, `/jwt/v1/accounts/*` (NATS account
resolver), OAuth authorize/callback/config, invitation get/accept; Basic
-- `GET /api/v1/login`; Bearer -- roles, apitokens, accounts/me/apitokens
reads, users/me/*; RequireAdmin -- account-local user/invitation/activity
management, accounts/me/apitokens writes; RequireSuperAdmin -- accounts,
services, agents, serviceendpoints, pki, proxy, logs, metrics.

List routes (`GET /api/v1/accounts`, `users`, `activities`, `services`,
`agents`, `serviceendpoints`, `pki/certificates`, `apitokens`,
`accounts/me/apitokens`, `invitations`) answer the complete collection,
sorted (accounts and services by name, users by email, agents by
hostname, activities and invitations newest first, tokens by name), unless
the request names `limit` (1 to `apicontract.MaxPageLimit`, 200) or
`offset`. Then the body is a page object: `apicontract.Page` (`total`,
`limit`, `offset`, `has_more`, `next_offset`) plus `items`; the service
endpoint page lists the endpoints that the complete form keys by kind.
Out-of-range values are a 400. The two forms exist so the console, the
agent CLI and older SPAs keep their arrays while the assistant (whose
tool results are bounded to 32 KiB) and scripts page; `admin.writeList`
is the one place that decides, and a new list handler should go through
it. `@bboxsys/ui` exports the `Page` and `ListPage<T>` types. Logs, store
metrics history and AI usage runs page the way they always did.

`admin.Store` wraps `store.Store` with typed methods for users, passwords,
agents, services, operators, accounts, activity, endpoints, proxy config,
API tokens, OAuth identities/state, and invitations, plus the secondary
indexes `user_email_index:{email}`, `oauth_index:{provider}:{subject}`,
`invitation_index:{token}`. `LogStore`/`MetricsStore` are rolling BadgerDB
buffers (prune to MaxEntries) fed by the NATS log/metrics subjects.

### API tokens (bring-your-own credential vault)

`admin.APIToken` stores a credential the caller supplies (`Type`, `ID`,
`Token`, optional `Metadata{ResourceType, ResourceID, ResourceName,
Endpoint, Tags}`) and never returns the secret: every handler responds with
`APITokenView`, which has `has_token` instead of `token`. The secret is
sealed with AES-256-GCM before it is written (key: `AdminStoreOptions.
SecretsKey`, else `SecretsKeyFile`, else `secrets.key` beside the store,
created on first run). Records written before sealing existed read back
unchanged and are sealed on their next write.

`Scope` is `user` (owner only; the default, and what every pre-scope record
normalizes to) or `account` (visible to and usable by everyone in the
account, managed by administrators). The scope is fixed by the route, never
by the request body:

| Route | Auth | Scope |
|---|---|---|
| `GET/POST /api/v1/apitokens`, `GET/PUT/DELETE /api/v1/apitokens/{name}` | Bearer | user (the list also includes account tokens, each tagged with `scope`) |
| `GET /api/v1/accounts/me/apitokens[/{name}]` | Bearer | account |
| `POST /api/v1/accounts/me/apitokens`, `PUT/DELETE .../{name}` | RequireAdmin | account |

Storage key is `{accountID}:apitoken:{owner}:{name}` where owner is the
user's NATS public key or the reserved `_account` segment; a user-scoped
and an account-scoped token may share a name. `PUT` with `token` absent,
empty, or `"********"` keeps the stored secret; zero `metadata` keeps the
stored association. Names must be non-empty, at most 128 characters, and
free of `: / \ ? #`. Create, update, and delete are logged with the acting
user's public key and email.

Backend code that USES a credential on a user's behalf reads it from the
store, never from the API: `GetAPITokenVisibleTo(accountID, userID, name)`
(user token first, then account), `GetAPITokensVisibleTo`,
`GetAPITokensVisibleToByType`, `GetAPITokensByResource(accountID, userID,
resourceType, resourceID)`. `GetAPIToken`, `GetAPITokensByUser`,
`GetAPITokensByType`, `DeleteAPIToken` stay user-scoped; the account
variants are `GetAccountAPIToken`, `GetAccountAPITokens`,
`DeleteAccountAPIToken`. `PutAPIToken` validates and seals; it does not
mutate the caller's copy. Whether a stored credential still works is a
product question (what counts as a successful probe), so validation
endpoints belong in the service that hosts the admin server, reading the
credential in-process.

#### Renewable OIDC credentials (admin/apitoken_oidc.go, oidctoken)

Full reference: [OIDC_API_TOKENS.md](OIDC_API_TOKENS.md).

A token's secret is either the bearer itself or the durable OIDC inputs a
bearer is minted from. The mode is the `oidc_auth_mode` tag
(`APITokenAuthModeTag`): absent or `static` means `Token` is the bearer;
`client_credentials`, `password`, and `refresh_token` are OAuth grants. For
the OIDC modes the client ID is the token's `ID`, the non-secret settings
are tags (`oidc_issuer`, `oidc_scopes`, `oidc_username`, `oidc_token_field`
= access_token|id_token, `oidc_client_auth_method` =
auto|client_secret_basic|client_secret_post|none,
`oidc_insecure_skip_verify` for the identity provider only), and the client
secret, password, and refresh token are one JSON document
(`APITokenOIDCSecret{version:1, client_secret, password, refresh_token}`)
in the sealed `Token`. `@bboxsys/ui` reads and writes exactly this shape
(`readAPITokenAuth`, `withAPITokenAuth`, `ApiTokenAuthFields`), so a
product only adds its own resource type, endpoint, and tags.

```go
resolver := admin.NewAPITokenResolver(store)          // once per process
token, _ := store.GetAPITokenVisibleTo(accountID, userID, name)
bearer, err := resolver.Bearer(ctx, accountID, token) // static or minted
```

`APITokenResolver.Bearer` returns a static token as stored and mints an
OIDC one through a shared `oidctoken.Manager`: discovery from the issuer's
`/.well-known/openid-configuration` (issuer must match, https only, no
redirects), the grant against the advertised token endpoint, a cache keyed
by every input that renews 60s before expiry (or a fifth of a short
lifetime) and collapses concurrent mints, and a rotated refresh token
sealed back with `PutAPIToken` before it is needed again. `Forget(token)`
drops the cached bearer when the resource server rejects an unexpired one.
`token.AuthMode()`, `token.OIDCSource()`, and `Metadata.Flag(tag)` are the
building blocks. `oidctoken` itself has no admin dependency
(`Source`, `Mint`, `Manager`, `Expiration`) and `oidctoken/oidctokentest.
NewIssuer(t, authMethods...)` is a TLS fake provider for tests.

`admin/email`: `Sender` interface (`SendInvitation`), `NewSMTPSender`
(STARTTLS submission; Google Workspace app password in production),
`NewNoopSender` (logs the token). SMTP is chosen only when host, username,
password, from, AND invite_accept_url are all set.

---

## 4. Identity model (user)

Pure data/crypto; no storage, no HTTP. Everything is a NATS nkey identity
with JWT claims.

- `UserBase{ID, Roles, PublicKey, Claims, Token, Seed, AccountID}`;
  `User` adds email/name/OAuth/profile fields. Store ID is
  `"{accountID}:{email}"` (`GetID()`); the NATS public key is separate.
- `NewUser(userConfig, accountSigningKeyPair) (*User, *UserPassword, error)`
  -- bcrypt cost 12. `ValidatePassword(hash, pw)`.
- Tokens: `NewUserToken(user, minutes, keyPair)`,
  `user.CreateToken(minutes, keyPair)`,
  `ValidateUserToken(token, accountPublicKey) (*UserClaims, error)`
  (errors when issuer != that account key).
  CAVEAT: `DecodeUserClaims` does NOT populate the custom `User`/`Roles`
  fields from the JWT (the roles appear only as claim tags) -- role checks
  must go through the store (`GetUserByPublicKey` + `HasRole`), which is
  what `Auth.RequireRole` does. `GetUserByPublicKey` falls back to `agent`
  and `service` records, so an agent or service JWT with roles passes the
  same gates a user would; both have no roles unless granted.
- NATS subject permissions: `UserConfig.Permissions *NATSPermissions`
  (`publish_allow|publish_deny|subscribe_allow|subscribe_deny`) is written
  into the JWT and enforced by the controller; nil means unrestricted.
  `ReadOnlyAPIPermissions(services...)` = `BaselinePublishSubjects()`
  (`_INBOX.>`, `$SRV.>`, `sys.log.>`, `sys.metrics.>`) plus
  `APIPublishSubjects(service, "GET", "HEAD")`. Once an allow list exists
  everything else is denied, so always include the baseline.
  `UserBase.Reissue(accountKeyPair)` rebuilds the claims from the current
  `Roles` and `Permissions` and signs a new `Token` for the same nkey.
- Roles: `RoleAdministrator|RoleUser|RoleViewer`; `HasRole` accepts legacy
  plurals (administrators/users/viewers).
- `Account`/`Operator` with `New*`, `GetSigningKeyPair()`.
- `Service`/`Agent` (also nkey users); `Service.Credentials()` and
  `Credentials{AccountID, JWT, Seed, TrustedAccounts, PrivateKey,
  Certificate, CA}` -- what `ServiceCredentials` returns for messenger
  bootstrap.
- `Invitation` (token, TTL default 72h, status pending/accepted/expired/
  revoked, `CanBeAccepted()`), `OAuthIdentity`/`OAuthState` (Google),
  `Activity` (login/lockout accounting; `Locked` is sticky until unlock).

---

## 5. Process composition

**supervisor** -- named long-lived goroutines with ready/close barriers:

```go
svisor := supervisor.NewSupervisor()
rc := svisor.Register("admin-server")
go adminServerRoutine(c, rc)
svisor.Get("admin-server").WaitReady()   // dependency ordering
svisor.WaitAllReady()
// shutdown: svisor.SignalAllClose(); svisor.WaitAllClose()
```

The routine contract every service follows:

```go
func xRoutine(c *config.Config, rs *supervisor.RoutineSupervisor) {
    defer rs.Done()
    // construct; go srv.Start()
    // poll srv.Ready() on a 10ms ticker, abort on <-rs.HasBeenClosed()
    rs.SetReady()
    <-rs.HasBeenClosed()
    srv.Close()
}
```

Typical routine names: ui-server, admin-server, controller-server,
proxy-server, queue-server, media-server, proxy.
Dependency edges: controller waits on admin; queue/proxy wait on
controller; media/proxy wait on admin.

**controller** -- embedded NATS server with JWT auth: trusts the operator
JWT and resolves account JWTs from the admin service's
`/jwt/v1/accounts/` URL.

```go
controller.NewController(controller.ControllerConfig{
    Application,                            // app.name, for log lines
    Port, MonitorPort, OperatorJWT, SystemAccountID,
    ResolverURL, ResolverServerName,        // ServerName = SNI through a TCP proxy
    KeyFile, CertFile, CAFile, InsecureSkipVerify,
})
// Start() blocks until ReadyForConnections (Fatal on 10s timeout); Close()
```

The resolver connection trusts the system roots plus `CAFile`, so an admin
server with a private or self-signed certificate verifies as long as that
certificate (or its CA) is the configured `security.ca_file`.
`InsecureSkipVerify` should not be needed outside throwaway setups.

OperatorJWT/SystemAccountID come from config or, in the all-in-one
process, from `adminServer.GetOperatorJWT()` / `GetSysAccountID()`.
The standalone `cmd/admin` does the same when `controller.embedded = true`
and `controller.port` is set: it starts the controller after the admin
server is ready (the controller fetches the system account JWT from
`/jwt/v1/accounts/` at startup), passes `controller.monitoring_port` to
`SetMonitoringPort`, and, with `admin_server.messenger = true`, connects
its own messenger client. Whenever a controller is configured it also
feeds the Logs and Store metrics pages: `adminServer.StartCollectors`
(`admin/collectors.go`) subscribes to `sys.log.>` and `sys.metrics.>`
once per account, as that account's `log_collector` service user, and
follows accounts as they are created and deleted. NATS accounts isolate
subjects, so a single SYS subscription would never see a host agent or a
tenant-scoped service publishing inside its own account. On top of that,
`cmd/admin/collectors.go` attaches SYS-account publishers to the admin and
controller loggers and starts a periodic metrics publisher for `admin.db`,
`logs.db`, and `metrics.db`. Without both the stores open but stay empty.
`GET /api/v1/logs` and `/logs/count` (super admin) filter by `service`,
`host`, `level` (lowercase), `search` (substring on the message), and a
time window: `since` and `until` take RFC 3339, Unix seconds or
milliseconds, or a duration such as `15m` meaning that long before now.
The list route pages with `limit` (default `DefaultLogLimit`, 200) and
`offset`, newest first. `GET /api/v1/logs/summary?by=message|service|level|
host|component&limit=10` returns the largest groups under the same
filters, the one-call answer to "which messages are most common". These
routes are the assistant's `admin_list_logs`, `admin_count_logs`, and
`admin_summarize_logs` tools, so their OpenAPI descriptions are what the
model reads.
With `admin_server.nats_api` (default true once a controller is
configured) the admin API is also served over NATS as `admin_service` on
those same per-account connections (section 12).
`logging.level` from the config is applied at startup. `admin -init`
writes the controller section into a development profile on the two ports
after the admin port and keeps `security.ca_file` pointed at `trust.pem`,
a bundle of the localhost certificate plus the dev root and intermediate
CA, so the controller accepts agent certificates and agents trust it.

The host agent (`host.Agent.Start`) connects to `controller_url` from its
registration with the credentials the admin server issued and publishes
its loggers' events as service `agent` on `sys.log.agent` inside its
tenant account; `agent/run.go` attaches the command logger too. The agent
configuration's `ca` carries the issuing chain plus the admin server's
own CA (`security.ca_file`), which is what the agent needs to verify the
controller. A configuration saved without `controller_url` runs with
local-only logging and says so; `./run-agent.sh` re-registers such a file.

With `queue_server.port` set as well, `cmd/admin/queue.go` runs the job
queue relay (section 9) in-process as the `queue` service account, adds
its `queue.db` to the metrics publisher and its logger to the log
publishers, and the admin publishes it as `queue_service`. With
`queue_server.nats_api` (default true) its API is served over NATS as
`queue_server` inside the `queue` account. `admin -init` seeds it on the
port after the monitoring port.

**host** -- host facts (`host.GetHostname()` FQDN) and the on-host agent
(`AgentConfigFromFile`, `NewAgent`, `Start`) with SR-IOV PF/VF management
on Linux.

**agent** -- the headless host agent CLI built on `host`. A product ships
its own binary by branding the command:

```go
import "github.com/bboxsys/platform/agent"

func main() {
    if err := agent.Execute(agent.Options{Name: "acme-agent"}); err != nil {
        os.Exit(1)
    }
}
```

`agent.NewCommand(opts)` returns the cobra root (`--config`, `--as-service`)
with four subcommands: `register`, `run`, `api`, and `services`.
`register -a <accountID> -u <adminServerURL> -t <token>`
POSTs `{account_id, hostname, host_id}` to `/api/v1/agents/configuration`
(with `X-Bboxsys-AccountID` and a bearer token) and writes the returned
agent configuration verbatim to `--config`, defaulting to
`config.DefaultAgentConfigFullPath()` (`blackbox-agent.json`; `--as-service`
selects /etc/blackbox). The file is JSON whatever its extension. `run` loads
it with `host.AgentConfigFromFile`, applies PF configs, starts the agent and
blocks: an in-place write to the file closes the running agent and starts a
new one from the updated file, SIGINT/SIGTERM close it and exit. Editors
that replace the file by rename are not detected by the watcher.
Registration skips TLS verification because the host does not trust the
internal PKI until registration delivers the CA. `cmd/agent` is the
unbranded build; a product wraps it under its own name.

`api METHOD PATH` calls any service's HTTP API over the controller with
the agent's own credentials (section 12): `--data` takes inline JSON,
`@file`, or `-` for stdin, `--service` picks the target (default
`admin_service`), `--header 'Key: value'` repeats, and `--timeout` bounds
the wait per response message. The body goes to stdout, the status line
to stderr, and the exit code is 1 for a status of 400 or more, a path
outside `/api/`, or no responder. `services` lists what answers the nats
micro `$SRV.INFO` request in the agent's account, one line per service
with its endpoint subjects, or the raw documents with `--json`. Both read
the registration file, so they work wherever `run` works.

What the agent may do is decided at registration. The configuration
request accepts `roles` (an agent with `administrator` passes
`RequireAdmin` through the identity fallback of section 4) and
`permissions` (a `NATSPermissions` allow list the controller enforces,
for example read-only access to one service). Re-registering an existing
host with either field updates the agent and reissues its token;
re-registering without them leaves it untouched. The same fields exist on
`POST /api/v1/services` and `POST /api/v1/agents`.

The registration endpoint is super-admin only (`RequireSuperAdmin`), reads
the account from `X-Blackbox-AccountID`, and signs the per-host certificate
with the admin server's intermediate CA, so
`admin_server.intermediate_{ca,cert,key}_file` must be configured or the
server returns 500 with "no intermediate CA configured". `admin -init`
seeds a development root and intermediate in the profile directory and adds
those keys to the TOML, and `./run-agent.sh` exercises the whole flow
against `./run.sh`: it logs in as the default super admin, registers with
that account's ID and session token, writes
`~/.config/agent-test/blackbox-agent.json`, and runs the agent.

---

## 6. Config

`config.New(kind)` (kind "user" -> ~/.config/blackbox, "service" ->
/etc/blackbox, "test" -> ~/.gounittests) + `FromFile()` loads
`blackbox-server.{toml,json,...}` into `c.V` (viper). Precedence:
`--config` flag -> `$BBOXSYS_CONFIG` -> default path. Services read keys
directly (`c.V.GetString("admin_server.url")`) rather than unmarshalling.

`ConfigTemplate` + `DefaultConfig(fqdn, kind, adminPort)` +
`WriteToDisk[TOML]` generate the initial config (used by `init` commands).
Sections: app (name, root_path, per-service URLs, account credentials),
admin_server (port, admin email/temp password, OAuth, SMTP, invites),
ui/proxy_server/queue_server/host_server/blackbox_server/media_server
(ports + per-service options), controller (NATS ports, account server,
operator), security (browser + internal cert/key/ca files), lets_encrypt
(provider, domains, challenge type, dns credentials, cert cache,
apply_to list), proxy (SNI routes), logging, metrics.

---

## 7. Logging and telemetry

`logger.NewLogger(application, component)` -> `Debug/Info/Warn/Error/
Panic/Fatal(msg string)`, `LogRequest` middleware, `SetLevel`,
`ZeroLogger()`. `AttachHook` + `NewNATSHook(&NATSHookOptions{Publisher,
Service, Component, MaxPerSec})` mirrors log events to NATS
(`sys.log.{service}`, rate-limited, fire-and-forget; `*messenger.Client`
satisfies `NATSPublisher`). Third-party libs with their own logging get
adapters (see `letsencrypt.NewLegoLogger`).

`logentry.LogEntry{ID(ulid), Timestamp, Service, Level, Message,
Component}` with `NATSSubject(service)`/`NATSSubjectWildcard`
(`sys.log.>`); the admin host subscribes raw and feeds
`LogStore().PutLogEntry`.

`storemetrics.StoreMetricsEntry{..., Metrics store.Metrics}` on
`sys.metrics.{service}`; `NewPublisher(service, []StoreInfo{{Name,
Store}}, interval, messengerClient, logger).Start()` snapshots every
store on a ticker; admin host feeds `MetricsStore().PutMetricsEntry`.

---

## 8. NATS client (messenger)

```go
mc, _ := messenger.NewClient(&messenger.ClientOptions{
    Application, Component, AccountID,
    PrivateKey, Certificate, CA,      // mTLS PEMs
    JWT, Seed,                        // from admin ServiceCredentials
    ControllerURL,                    // TLS ServerName derives from its host
})
mc.Connect()                          // FATAL (exits) on failure
mc.Publish(subject, obj)              // JSON-encoded conn
mc.Subscribe(subject, func(obj interface{}) {})   // typed handlers work too:
//   Subscribe(subj, func(subj, reply string, msg *SomeStruct) {})
mc.PublishRaw / mc.SubscribeRaw       // byte pipelines (logs/metrics)
mc.RegisterWorker(subject, cb)        // QueueSubscribe group "workers"
messenger.NewClientFromConn(nc, app, component)  // wrap an existing *nats.Conn
                                      // (embedded server, tests); no mTLS/JWT
```

Reconnect: retry-on-failed-connect, wait 10m, max 600 reconnects.

---

## 9. Asynchronous jobs

`job` is the message model, `worker` the receiving side, and `queue` an
optional relay service of a small job bus over the tenant's NATS account.
Three subjects carry it:

| Subject | Published by | Meaning |
|---|---|---|
| `job.request.{service}.{name}` | `Job.Dispatch` | A producer asks for the job to run; the queue relay records it |
| `job.queue.{service}.{name}` | `Job.Enqueue` (relay or producer) | Offered to the worker queue group; exactly one worker takes it |
| `job.status.{id}` | `Job.DispatchStatus` / `Worker.Run` | Progress for one job: `started`, `complete`, `error` (`waiting`, `cancelled` reserved) |

```go
// Producer
j := job.New("media", "thumbnail", thumbnailArgs{ItemID: id, Size: 256})
sub, _ := j.SubscribeStatus(mc, func(s *job.Status) { ... })   // optional
j.Dispatch(mc)      // through a relay that stores requests (package `queue`)
j.Enqueue(mc)       // or straight to the workers, no relay needed

// Worker process (any number; each job reaches one of them)
w := worker.New(&worker.Options{Service: "media", JobName: "thumbnail", MC: mc, Logger: l})
w.Run(func(j *job.Job) (string, error) {
    var p thumbnailArgs
    if err := j.DecodePayload(&p); err != nil { return "", err }   // payload arrives as generic JSON
    return render(p)                                              // result text goes in the complete status
})
// w.Subscribe(cb) is the raw form: cb reports status itself via w.DispatchStatus.

// Relay service: one per deployment, in its own supervisor routine
qs, _ := queue.NewQueueServer(&server.ServerOptions{
    Application, Component: "queue_server", HTTPPort, PrivateKey, Certificate, CA,
    Messenger: true, MessengerServerURL, MessengerJWT, MessengerSeed, TrustedAccounts,
    Metadata: map[string]interface{}{"storeLocation": root + "/queue.db"},
})
go qs.Start()            // relay + REST; qs.Ready(), qs.Close(), qs.GetBaseStore() for metrics
```

The relay (`queue.Queue`) subscribes to `job.request.*.*`, stores each job,
and republishes it with `Enqueue`; it subscribes to `job.status.*` and keeps
the latest status per job. Its REST surface, all bearer-authenticated:
`POST /api/v1/queue` takes `{service, name, payload}` and answers
`{"status": "created", "id"}`; `GET /api/v1/queue` lists every job with its
latest status, oldest first; `GET /api/v1/queue/{id}` returns one. The
store (`queue.Store`) is also usable directly: `PutJob`, `PutJobStatus`,
`GetRecord`, `ListRecords`.

The same REST surface is served over NATS as `queue_server` inside the
queue account when `ServerOptions.NATSAPI` is set (section 12), so a
worker or agent in that account can submit and read jobs with
`agent api POST /api/v1/queue --service queue_server`.

The admin SPA's Queue page (Platform section, super admins) is a table of
those records. It reads them through the admin server, which proxies
`GET /api/v1/queue` and `GET /api/v1/queue/{id}` to the `queue_service`
endpoint from its service endpoint map, forwarding the caller's bearer and
`X-Blackbox-AccountID` headers so the queue service authenticates the user
itself. No endpoint published answers 503; an unreachable queue answers 502.

- `Run` publishes `started` before the handler and `complete` (with the
  returned string as `Result`) or `error` (with `Errors`) after; a handler
  panic becomes an `error` status and the worker keeps running.
- Handlers run on the subscription goroutine, one job at a time per
  process. Run more processes for more parallelism.
- IDs are ULIDs (`job.NewID`), so they sort by creation time. `Status`
  carries `Errors []string`; `StatusOptions.Errors []error` is flattened.
- Every call returns `job.ErrNotConnected` when the client has no NATS
  connection instead of panicking. Subscriptions are returned so callers can
  `Unsubscribe`.
- All three packages are product-neutral. Tests spin up an embedded
  `nats-server` and wrap the connection with `messenger.NewClientFromConn`;
  the queue tests build a `QueueServer` around that client and a temp store
  without TLS.

---

## 10. PKI and Let's Encrypt

`pki`: `NewCA(name, org)` (10-year self-signed root),
`NewIntermediate(caFile, certFile, keyFile, org)` +
`Intermediate.NewCertificate/NewTLSCertificate/Chain()`,
`NewSelfSignedCertificatePair`, PEM/DER parse/write helpers,
`CertInfoFromFile` (+`Status()` valid/expiring/expired, `DaysRemaining`),
OS trust store `InstallCA`/`UninstallCA` (darwin/linux).

`letsencrypt.NewManager(config.LetsEncryptConfig, zerolog)` ->
`Initialize()`, `EnsureCertificate()`, `GetTLSConfig()` (SNI
GetCertificate), `StartAutoRenewal(12h)`, `Stop()`. HTTP-01 or DNS-01
(cloudflare, route53, digitalocean, godaddy, namecheap; credentials JSON
file). Shared-manager pattern: the all-in-one process creates ONE manager
and hands it to each `ServerOptions.LetsEncryptManager`; servers with a
shared manager do not start/stop renewal themselves.

---

## 11. SPA dev servers

Every consumer SPA signs in through this module's admin server and finds it
in the service endpoint map the hosting server publishes at
`/serviceendpoints/` (`login_service`, derived from `app.hostname` plus
`admin_server.port`, or an explicit `app.login_service`). In development the
SPA runs on a Vite port and proxies that route to a running server, so it
stays same-origin exactly as it is when embedded.

`@bboxsys/ui/vite` supplies both halves, so every app configures this the
same way:

```ts
import { devEndpoints, devProxy } from '@bboxsys/ui/vite'

const devServer = { fallback: 'https://localhost:8880', env: 'MYAPP_UI_SERVER', project: 'myapp' }

export default defineConfig({
  plugins: [vue(), devEndpoints(devServer)],
  server: { proxy: devProxy(['/serviceendpoints'], devServer) },
})
```

- `ADMIN_PROXY_TARGET_DEV` moves the proxy target -- which server the dev
  SPA asks for the endpoint map (and, for the admin SPA, `/api` and `/jwt`).
  The app's own variable (`ADMIN_PROXY_TARGET`, `MYAPP_UI_SERVER`) still works as the next
  fallback.
- `BBOX_DEV_ENDPOINTS` rewrites entries of the map itself. This is the knob
  that changes where the SPA signs in: a bare URL repoints
  `login_service` + `admin_service`, and `kind=url` pairs repoint anything
  else. Unlisted kinds keep what the server published.

The two are not interchangeable, and that catches people out: the proxy
target is often a different port from the admin server (a product
may serve the SPA on 8880 and the admin API on 8900), and the URLs inside the
map always win over where the map was fetched from. Pointing the proxy at
another port cannot change the sign-in URL; only `BBOX_DEV_ENDPOINTS` or the
server's own `app.login_service` can.

Full reference, including the
`~/.config/{project}-test/dev-endpoints.json` file form: `ui/docs/DEVELOPER.md`.

## 12. The API over NATS

`natsapi` carries a service's whole HTTP API over NATS request/reply, so a
host agent or an AI service that knows only the controller and holds its
own credentials can call the same endpoints as the SPA: same verbs, paths,
headers, payloads, and status codes. It is a bridge, not a second API. On
the service side each request becomes an `*http.Request` dispatched through
the service's normal router, so the ID middleware and every `Bearer`,
`RequireAdmin`, and `RequireSuperAdmin` wrapper run unchanged and
permissions cannot drift from REST.

**Wire format.** One request message on
`api.{service}.{METHOD}.{resource}`, where `resource` is the first path
segment after `/api/v1/` (`natsapi.ResourceToken`). Headers are the HTTP
headers plus `X-Path`, the full path and query, which is authoritative; the
subject token exists only so a JWT can restrict a caller to, say,
`api.admin_service.GET.>`, and the service refuses a request whose token
does not match its path. Responses stream on the reply inbox in chunks
below the NATS max payload: the first message carries `X-Status` and the
response headers, each carries `X-Seq`, the last carries `X-End`. A request
body larger than one chunk is pulled: the caller publishes `X-Body-Stream`
naming a subject it serves and the service requests chunks from it. Store
list endpoints that stream to the `ResponseWriter` therefore stream over
NATS too. Every bridged request carries `X-Transport: nats` and a
`RemoteAddr` of `nats:{accountID}`.

**Identity.** The bearer is the caller's own NATS user JWT: an agent's
`AgentConfiguration.JWT` or a service's `ServiceCredentials` JWT is signed
by the tenant account and validated by `Auth.Bearer` exactly like a login
token. NATS adds a check REST cannot make: the service knows which account
each of its connections belongs to and refuses (403) a request whose
`X-Blackbox-AccountID` names another account; when the header is absent it
is filled from the connection. Agents and services pass role gates through
`Store.GetUserByPublicKey`, which falls back to `agent` and `service`
records; both have no roles unless they were granted at registration
(`roles` in `POST /api/v1/agents/configuration` or `POST /api/v1/services`;
re-registering a host with roles or permissions updates the agent and
reissues its token).

**Serving (any service built on `server`):**

```go
opts.Messenger = true          // the API rides on the messenger connection
opts.NATSAPI = true            // subscribe as api.{Component}.{METHOD}.>
opts.NATSAPIService = "media_service"   // optional; default is Component
srv.Serve()                    // starts it; srv.NATSAPI is the natsapi.Service
```

NATS accounts isolate subjects, so a service connected in one account
hears only that account. A multi-tenant service follows the tenants with
`srv.FollowAccounts(provider, accounts)`: it opens one connection per
account with credentials from the provider, serves the API on each, and
tracks `tenant.notify.created|deleted` on the primary connection. The
admin server hosts the store, so it rides on its collectors instead: call
`adminServer.StartNATSAPI(admin.NATSAPIOptions{})` before or after
`StartCollectors` and every account the collectors follow (including the
ADMIN account, so super admins too) is served as `admin_service`.
`cmd/admin` turns it on with `admin_server.nats_api` and the queue relay
with `queue_server.nats_api`, served as `queue_server` inside the queue
account; both default to true when a controller is configured and
`admin -init` seeds them.

**Calling (Go):**

```go
client := natsapi.NewClient(mc.Client.Conn)                 // *http.Client
req, _ := natsapi.NewRequest(ctx, "GET", "admin_service", "/api/v1/users", nil)
req.Header.Set("Authorization", "Bearer "+cfg.JWT)
resp, err := client.Do(req)        // natsapi.ErrNoService when nobody answers
```

`natsapi.Transport` is an `http.RoundTripper`, so code written against
`*http.Client` works unchanged with `nats://{service}/api/...` URLs. From a
registered host the agent CLI does the same with its own credentials:

```
agent api GET /api/v1/users
agent api POST /api/v1/users --data @user.json      # or --data '{...}' or --data -
agent api GET /api/v1/queue --service queue_server  # default service is admin_service
agent services [--json]                             # what answers $SRV.INFO in the account
```

The body goes to stdout, the status line to stderr, and the exit code is
1 for a status of 400 or more, a path outside `/api/`, or no responder.

**Discovery.** Every served connection is a nats `micro` service, so
`$SRV.PING`, `$SRV.INFO.{service}`, and `$SRV.STATS.{service}` answer inside
each account with one endpoint per verb and `metadata.account`.

**Scoping a caller.** A JWT with a publish allow list is limited to those
subjects by the controller itself, before any handler runs.
`user.ReadOnlyAPIPermissions("admin_service")` builds the allow list for a
read-only caller: `api.admin_service.GET.>` plus the baseline every agent
needs (`_INBOX.>`, `$SRV.>`, `sys.log.>`, metrics). Pass it as
`Permissions` in `AgentConfigurationOptions` (`POST
/api/v1/agents/configuration`) or `UserConfig`; an empty permissions set
means unrestricted, as before. Roles for an agent go in the same request.

**Limits.** Only `/api/` paths are bridged; the SPA, websocket, OAuth,
and Basic login routes are not. The service runs each request on its own
goroutine, instances of a service share queue group `api`, and the client
waits `Transport.Timeout` (30s) for each response message. All packages
test against an embedded `nats-server`; `natsapi/natsapi_test.go` is the
reference for wiring a router behind the bridge.

---

## 13. AI assistant (ai)

`ai` and its subpackages are a tenant-aware agent any service can host:
an agent loop over an append-only history, a model provider, tools
generated from the service's own OpenAPI document, retrieval and memory
on an embedded vector store, and MCP in both directions. Model and
embedding keys are API tokens in the admin vault. The full design,
including the walkthrough for configuring a model and the Claude Code
MCP example, is `docs/AI.md`.

```go
// ai: the contract and the loop
type Tool interface { Name() string; Description() string; InputSchema() json.RawMessage; ReadOnly() bool
    Execute(ctx context.Context, input json.RawMessage) (Result, error) }
ai.NewTool(name, description string, schema json.RawMessage, readOnly bool, fn) *FuncTool
ai.WithCaller(ctx, ai.Caller{AccountID, UserID, Bearer}); ai.CallerFromContext(ctx); ai.CallerFromRequest(r)
type Provider interface { Name() string; Model() string; UserMessage(string) Message
    ToolResultMessage([]ToolResult) Message; Turn(ctx, TurnInput, emit func(Event)) (*Turn, error) }
type ProviderFactory func(cred Credential) (Provider, error)
type Checker interface { Check(ctx) (CheckResult, error) }          // optional: a token-free credential test
type ModelLister interface { ListModels(ctx) ([]ModelInfo, error) } // optional: the endpoint's models, for the form
type CredentialResolver interface { Model(ctx, accountID, userID, name string) (Credential, error)
    Embedding(ctx, accountID, userID, name string) (Credential, error)
    MCPServers(ctx, accountID, userID string) ([]Credential, error) }
type CredentialLister interface { Models(ctx, accountID, userID string) ([]Credential, error) } // optional: Status.models
type SessionStore interface { Put; Get(ctx, accountID, userID, id); List(ctx, accountID, userID); Delete }
ai.NewStoreSessions(st *store.Store) SessionStore   // {accountID}:ai_session:{userID}:{id}; NewMemorySessions for tests
ai.New(ai.Options{Provider, Tools, System, MaxTurns, Approve, MaxToolResultBytes, Logger}) (*Agent, error)
(*Agent).Run(ctx, session *Session, prompt string, emit func(Event)) error
ai.RepairDanglingToolCalls(session, provider) bool  // answer an interrupted run's tool_use with error results
ai.NewService(ai.ServiceOptions{Credentials, Providers, Sessions, Tools, System, Preamble,
    MaxTurns, SetupTimeout, AllowWrites, AllowTools, Usage, Logger}) (*Service, error)
ai.StaticCredentials{ModelCredential, EmbeddingCredential *Credential; MCPServerCredentials []Credential}
    // a CredentialResolver without the vault: host agents, products with a key in their own config
    // SetupTimeout default ai.DefaultSetupTimeout (20s, 504 when it fires); provider cache ai.MaxCachedProviders (256)
(*Service).Mount(router *mux.Router, prefix string, wrap func(http.HandlerFunc) http.HandlerFunc)

// ai/anthropic
anthropic.New(cred ai.Credential, anthropic.Options{Effort, MaxTokens, Fallbacks, DisableCompaction, HTTPClient, Logger})
anthropic.Factory(opts) ai.ProviderFactory          // default model claude-opus-5-5, effort medium, 16000 tokens

// ai/openai: the OpenAI chat completions dialect (Ollama /v1, llama-server, vLLM, LM Studio, gateways)
openai.New(cred ai.Credential, openai.Options{Effort, MaxTokens, HTTPClient, Logger})   // openai_compat: endpoint is the base URL, required
openai.NewOllama(cred, opts)                        // ollama: endpoint is the server root (default http://localhost:11434), /v1 appended
openai.Factory(opts), openai.OllamaFactory(opts) ai.ProviderFactory   // no default model; a credential test lists the server's models
ai.OllamaBaseURL(endpoint) string                   // server root for an ollama credential: default, trailing /api or /v1 stripped

// ai/apitool
apitool.FromOpenAPI(spec []byte, h http.Handler, apitool.Options{Prefix, Include, ReadOnly, BaseURL, MaxBodyBytes}) ([]ai.Tool, error)
    // read-only: Options.ReadOnly, then the x-ai-read-only extension, then GET/HEAD
apitool.NewRequestTool(h, opts) ai.Tool             // {Prefix}api_request, always a write; /api/ only, canonical paths, honors Include

// ai/rag
rag.Open(dir) (*Index, error); (*Index).Collection(accountID, purpose, embed) (*rag.Collection, error)
    // "{purpose}:{accountID}"; a per-caller handle, embeds only with that caller's embed
rag.Embedder(cred ai.Credential) (chromem.EmbeddingFunc, error)              // voyage, openai_compat, ollama
rag.Add(ctx, col, []rag.Document); rag.Query(ctx, col, text, n, where); rag.Delete(ctx, col, sourceID)
    // Add embeds every chunk before replacing the old ones; Query filters on embed_dim
rag.SearchTool(index, embed, purpose) ai.Tool       // search_documents, read-only

// ai/memory
memory.NewNotes(st *store.Store) *Notes; memory.NotesTool(notes) ai.Tool          // "memory"
memory.NewRecall(index, embed) *Recall; Remember, Recall, Forget, Preamble
memory.RememberTool(r) ai.Tool; memory.RecallTool(r) ai.Tool                      // "remember", "recall"

// ai/instance: how every host runs an assistant
instance.New(instance.Options{Name, Service, Dir, Credentials, Providers, Effort, System, MaxTurns,
    SetupTimeout, AllowWrites, OpenAPI, Handler, ToolPrefix, ToolFilter,
    Publisher, Local, UsageRetention, HeartbeatInterval, Version, Logger}) (*Instance, error)
    // own store {Dir}/ai.db and index {Dir}/index; usage recorder started
serverhost.New(srv *server.Server, opts) (*instance.Instance, error)  // ai/instance/serverhost: Publisher, Handler, Name, Logger from srv
(*Instance).Mount(r *mux.Router, prefix string, bearer, requireAdmin func(http.HandlerFunc) http.HandlerFunc)
    // service routes, documents (POST requireAdmin), credentials/test, credentials/models, and /mcp
(*Instance).Service() *ai.Service; Store() *store.Store; Usage() *usage.Recorder
(*Instance).Tools(ctx, caller) ([]ai.Tool, error); instance.CloseTools(tools); (*Instance).Close() error
instance.PathHasPrefix(path, prefix) bool  // "*" and "{x}" match one segment; for ToolFilter deny lists

// ai/usage
usage.UsageSubject(instance) // sys.ai.usage.{instance}; HeartbeatSubject: sys.ai.heartbeat.{instance}; SubjectWildcard sys.ai.>
usage.NewRecorder(usage.RecorderOptions{Instance, Service, Host, Version, Store, Publisher, Local,
    Retention, HeartbeatInterval, Provider, Model, Logger}) (*Recorder, error)  // implements ai.UsageSink
(*Recorder).Start(); Close(); Records(ctx, since, limit); Totals()

// ai/mcp
mcp.Handler(get func(*http.Request) ([]ai.Tool, error)) http.Handler  // stateless streamable HTTP
mcp.ConnectWithOptions(ctx, name, endpoint, bearer string, mcp.ClientOptions{TrustReadOnly, HTTPTimeout}) (*Client, error)
mcp.Connect(ctx, name, endpoint, bearer string) (*Client, error)  // zero options: untrusted, 30s HTTP timeout
(*Client).Tools(ctx); (*Client).Close()   // remote tools are writes unless TrustReadOnly
```

**Credential contract.** A credential is an API token whose metadata says
what it opens. Go constants in `ai/credential.go`, browser mirror in
`@bboxsys/ui` `src/lib/aiCredential.ts`; change both together.

| Field | `ai_model` | `ai_embedding` | `mcp_server` |
|---|---|---|---|
| `metadata.resource_type` | `ai_model` | `ai_embedding` | `mcp_server` |
| `metadata.endpoint` | base URL, empty for the public API | base URL, empty for the provider default | server URL (required) |
| tag `ai_provider` | `anthropic` (default) | `voyage`, `openai_compat`, `ollama` | |
| tag `ai_model` | default `claude-opus-5-5` | default `voyage-3` / `nomic-embed-text` | |
| tag `ai_effort` | `low` ... `max` (unless `ai.effort` is set) | | |
| tag `ai_max_output` | output token cap | | |
| tag `mcp_trust_read_only` | | | `"true"` trusts the server's `readOnlyHint` |
| `token` (sealed) | API key | API key | bearer |

`admin.NewAICredentialResolver(store)` implements the resolver: the
user's tokens before the account's, by name, `name ""` for the first, the
bearer through `APITokenResolver` (static or OIDC). Only `anthropic` is a
chat model provider; `openai_compat` is for embeddings only (an
OpenAI-compatible chat provider is not implemented yet).

**One way to host.** Every assistant (the admin's, a product's, a host
agent's) is an `ai/instance`. It owns its store (`{Dir}/ai.db`: sessions,
notes, usage records and outbox) and index (`{Dir}/index`), generates the
API tools, connects remote MCP servers (15 s to connect and list; a
repeated credential name keeps the user's, a colliding tool name is
skipped and logged), and serves the documents routes (ingestion answers
413 over 16 MiB), the credential test, and `/mcp` (`?accountID={account}`,
since MCP clients cannot send the account header; only read-only tools
plus `memory` and `remember` unless `AllowWrites`).

```go
inst, _ := serverhost.New(srv, instance.Options{
    Dir:         filepath.Join(root, "ai"),
    Credentials: resolver,   // admin.NewAICredentialResolver(adminServer.Store()) in a process hosting the admin,
                             // ai.StaticCredentials{...} without the vault
    OpenAPI:     spec, ToolPrefix: "media_",
})
inst.Mount(srv.Router, "", auth.Bearer, adminOnly)   // ID.Middleware already on the router
defer inst.Close()
```

**Usage.** Each run is stored in the instance for 30 days and, with a
`Publisher` (the host's `*messenger.Client`; `serverhost.New` sets it), published
on `sys.ai.usage.{name}`; a heartbeat with totals goes out every 60 s on
`sys.ai.heartbeat.{name}`. Unpublished records wait in an outbox and are
retried each heartbeat. The admin collectors subscribe to `sys.ai.>` in
every account into `ai_usage.db`; super admins read it on AI > Usage.
See `docs/AI.md` section 11.

**Admin hosting.** `adminServer.StartAI(admin.AIOptions{Dir, Providers,
Effort, System, MaxTurns, AllowWrites, Version})` builds the instance
`admin_service` in `{Dir}/ai` over the vault resolver, with `admin_*` tools
from the embedded OpenAPI document over `Srv.Handler`, and mounts it under
`/api/v1/ai` and `/mcp`. Its usage goes into the AI usage store in-process
(`Local`), so call `InitAIUsageStore` first. The tools exclude a deny list
(`aiDeniedRoutes`: service credentials, login, logout, register,
`users/*/token`, OAuth, OIDC authorize and callback, invitations by token,
`/jwt`, `/serviceendpoints`, the assistant's own routes, `/mcp`) and every
operation tagged `Auth`; `admin_api_request` refuses the same.
`AIInstance()` and `AIStore()` expose it (the store metrics list
`ai/ai.db` and `ai_usage.db`). On first start it moves sessions and notes
an earlier release kept in `admin.db`, and index collections kept directly
in `{Dir}/ai`, into the new layout. `cmd/admin` calls it unless
`ai.enabled = false`; `ai.allow_writes` and `ai.max_turns` tune it, and
`ai.effort`, only when set, overrides every credential's `ai_effort` tag
(otherwise the tag applies, then `medium`).

**Import boundary.** `ai/...` imports `admin/client`, `logger`, and
`github.com/bboxsys/store`, never root `admin` or a product; only
`ai/instance/serverhost` imports `server` (no cycle: `server` imports no
`ai` package), so a host agent can use `ai/instance` without linking the
HTTP server. Root `admin` imports `ai/instance` to host it. The SDKs
(anthropic-sdk-go, the MCP go-sdk, chromem-go) are linked only into
processes that import those subpackages.

**Append-only history.** `Message.Content` is the provider's native
content-block JSON and is never edited after it is appended: current
models bind reasoning blocks to the conversation that produced them.
Context pressure is handled by the provider's server-side compaction
(on unless `anthropic.Options.DisableCompaction`), not by trimming
history. A session is bound to its provider and model; anything that
would rewrite history is a new session. Unanswered tool calls are
answered, never removed: a turn that stopped with calls for a reason other
than `tool_use` gets `not run: ...` error results appended, and the
service runs `RepairDanglingToolCalls` on every session it loads. The
service locks a session (by account, user, and id) before loading it, and
a panic in a run becomes an `error` event.

**Approval.** The default denies every tool that is not read-only.
`AllowWrites` lifts that for all tools; `AllowTools` names the few whose
writes stay in the caller's own scope (`memory`, `remember`), so memory
works with writes off. API tools dispatch through the service's own
handler with the caller's bearer: there is never a tool-only path to a
store. Remote MCP tools are writes unless their credential sets
`mcp_trust_read_only = "true"`.

---

## 14. Embedded documentation (docsite, ai/docsearch)

A product ships its Starlight documentation inside its binary, serves it
at a prefix of its UI server behind a session cookie the SPA sets with
the user's bearer, and gives its assistant the pages as searchable,
citable sections. The Go half is `docsite` (load, keyword search, gated
handler) and `ai/docsearch` (corpus, vectors, tools); the browser and
Starlight half is `@bboxsys/ui` (`docsSite`, `DocsGate`,
`openDocsSession`). `docs/DOCSITE.md` is the reference, with the
checklist for a new product.

## 15. Gotchas

- `ID.Middleware` before `auth.*` -- always (401s everywhere otherwise).
- Over NATS the account is the connection's: `natsapi` refuses a request
  naming another account (403) and fills the header when it is absent.
  `StartCollectors` needs a log store, a metrics store, or a started NATS
  API, else it errors; `StartNATSAPI` works before or after it.
- NATS headers are case-sensitive; `natsapi` reads them through an
  `http.Header` on both sides, so use the exported constants when
  publishing raw messages. nats.go 1.54 surfaces no responders as
  `nats.ErrNoResponders`; the transport maps it to `natsapi.ErrNoService`.
- A publish allow list on a JWT denies everything else: a scoped agent
  needs `user.BaselinePublishSubjects()` or it cannot log or get replies.
- `UserIDFromContext` after Bearer is the NATS public key; store lookups
  use `GetUserByPublicKey(accountID, publicKey)`.
- `RequireRole` without a store only passes super admins.
- `NewAdminServer` panics on missing `Metadata["storeLocation"]` /
  `["tokenValidMinutes"]`.
- `NewStore` fails if `secrets.key` beside the store (or `SecretsKeyFile`)
  exists but is not a base64 32-byte key. Losing the key makes every sealed
  API token unreadable; such records are skipped by list calls and fail
  by-name reads.
- `ServeUI` must be called before `Start()` (AddHandlers reads the FS).
- A dev SPA's sign-in URL comes from the endpoint map, not the dev proxy.
  Use `BBOX_DEV_ENDPOINTS`; `ADMIN_PROXY_TARGET_DEV` only moves the proxy.
- Build tags: `-tags adminui` (admin SPA, this repo) vs `-tags ui`
  (a product's own SPA). Tags are global across modules -- that
  is why they differ.
- Root `admin` re-exports all of `admin/client` (compat.go aliases).
  Non-hosting services import `admin/client`.
- `messenger.Connect` and `controller.NewController` Fatal (exit) on
  failure; `supervisor.Get` Fatals on unknown name.
- `clientip.Middleware` has a known bug (discards the context-carrying
  request); `logger.LogRequest` calls `clientip.FromRequest(r)` directly,
  which works. Do not rely on `clientip.FromContext`.
- admin root `server_test.go`/`store_test.go` note in README is stale:
  admin tests were rewritten 2026-08-30 and pass.
- After adding an import of a module the workspace has not vendored yet
  (a new SDK, say), run `go work vendor`. The workspace vendor tree is
  gitignored, so nobody else's refresh brings it in, and the build fails
  with "inconsistent vendoring" until you do.
- An AI session is bound to the provider and model that created it. A
  credential that now resolves to another provider makes `SendMessage`
  answer 409; switching models or providers means a new session, never an
  edit of the stored history.
