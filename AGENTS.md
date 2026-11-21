# platform Developer Guide

The Black Box Systems control plane (admin server + Vue admin SPA) and the
platform packages its services are built on. Full API
reference and integration recipes: `docs/DEVELOPER.md`. Read it before
wiring a new service or changing any exported surface.

## Rules

- No emojis in code, comments, or docs. No AI-tool attribution in commits.
- **API boundary**: services that only authenticate requests import
  `admin/client` (conventionally aliased `admin`). Only a process that
  HOSTS the admin server imports the root `admin` package (it drags in
  BadgerDB, PKI, OAuth, and the SPA embed). `admin/compat.go` re-exports
  the whole client surface from the root package for legacy imports --
  keep it in sync when adding client symbols.
- **Middleware order is a hard requirement**: `ID.Middleware` on the
  router BEFORE any `auth.Bearer/Basic/Require*` handler wrapper.
  `Bearer` reads the account public key that `ID` put in the context.
- After `Bearer`, `UserIDFromContext` returns the user's NATS public key
  (claims.Subject), NOT the store ID `{accountID}:{email}`. Look users up
  with `GetUserByPublicKey(accountID, publicKey)`.
- `RequireRole`/`RequireAdmin` need an `IdentityStore` (AuthConfig.Store
  or `SetStore` later); without one only the super-admin account passes.
  `*admin.Store` satisfies the interface.
- Tokens are NATS user JWTs signed by an account nkey; validation is
  local (signature + issuer in TrustedAccounts). New tenants created at
  runtime reach services via the `tenant.notify.*` NATS subjects +
  `Auth.AddTrustedAccount` / server `AddTrustedAccount`.
- `ui/` is the shared Vue kit, published to npm as
  `@bboxsys/ui` on every `v*` tag (`.github/workflows/release.yml`). The
  session store, login card, and admin API types that product
  SPAs use live THERE, not in `admin/ui`. See `ui/AGENTS.md`.
- The admin SPA (`admin/ui/`, Vue + shadcn + @bboxsys/ui via
  `file:../../ui`) embeds behind build tag `-tags adminui` -- deliberately
  NOT `ui`, because Go build tags are global across modules and a product
  uses `ui` for its own SPA. It is one build for every host and takes its
  name, mark and brand from `GET /branding/`; a host writes those with
  `admin.HostBranding{...}.Metadata()`, which reads `brand-tokens.css`
  from the host's embedded SPA (emitted by the kit's `brandTokens()`
  plugin from the SPA's `src/brand.css`). Never mirror a brand in Go.
- API token secrets never leave the backend: handlers return
  `APITokenView` (`has_token`, no `token`), the store seals `Token` with the
  key in `secrets.key` beside the store, and scope (`user` vs `account`) is
  fixed by the route (`/api/v1/apitokens` vs `/api/v1/accounts/me/apitokens`),
  never read from the body. Backend code that uses a credential reads it
  through `Store.GetAPITokenVisibleTo` or `GetAPITokensByResource`.
- A token that authenticates with OIDC instead of a static bearer keeps
  its mode and settings in metadata tags (`oidc_auth_mode`, `oidc_issuer`,
  ...) and its client secret/password/refresh token as one sealed JSON
  document. Backends get the bearer from `admin.NewAPITokenResolver(store).
  Bearer(ctx, accountID, token)`, which mints and caches through package
  `oidctoken` (provider-neutral, no admin import) and seals rotated refresh
  tokens back. `@bboxsys/ui` owns the matching form and tag helpers
  (`ApiTokenAuthFields`, `readAPITokenAuth`, `withAPITokenAuth`,
  `validateAPITokenAuth`); products add only their resource type, endpoint,
  and extra tags. Keep the two in step; `docs/OIDC_API_TOKENS.md` is the
  reference.
- `server.SPAHandler` serves precompressed .br/.gz, immutable-caches
  `assets/`, and 404s missing asset-extension paths instead of SPA
  fallback (ChunkLoadError guard). Do not weaken these properties.
- `NewAdminServer` type-asserts `Metadata["storeLocation"]` (string) and
  `Metadata["tokenValidMinutes"]` (int) without ok-checks -- omitting
  them panics. See docs/DEVELOPER.md for the full metadata key list.
- `agent/` is the headless host agent CLI (register with the admin server,
  then run and follow its config file). Products build their own binary
  with `agent.Execute(agent.Options{Name: ...})`; `cmd/agent`
  is the unbranded build. Keep the agent free of product imports. The agent
  publishes its logs on `sys.log.agent` inside its tenant account; the
  admin collects per account (`StartCollectors`), never from SYS alone.
- Fatal-on-error paths that exit the process: `controller.NewController`,
  `messenger.Client.Connect`, `supervisor.Get` on a missing name.
- Admin list routes answer the complete sorted array unless the request
  names `limit` or `offset`, which turns the body into an
  `apicontract.Page` plus `items` (`admin/paging.go`, `writeList`). Keep
  new list handlers on it; the console reads the array form, the
  assistant pages.
- The admin serves `GET /api/v1/queue[/{id}]` to super admins by proxying
  the `queue_service` endpoint with the caller's bearer and account
  headers; the Queue page reads that, never the queue port directly.
- `natsapi/` bridges a service's HTTP API over NATS: a request on
  `api.{service}.{METHOD}.{resource}` with `X-Path` becomes an
  `*http.Request` through the service's own router, so REST permissions
  apply unchanged; bodies stream in chunks. `ServerOptions.NATSAPI` turns
  it on for any `server.Server`; `AdminServer.StartNATSAPI` serves
  `admin_service` on every collector connection. Agents call it with
  their own JWT as the bearer (`agent api`, `natsapi.NewClient`). Never
  add NATS-only handlers for store records: the handlers are the policy.
- Every AI host (admin, product service, host agent, app) goes through
  `ai/instance` (`instance.New`, or `serverhost.New` for a service on
  package `server`); never wire `ai.NewService`
  by hand. Each instance has its own store and index under its `Dir`
  (`{Dir}/ai.db`, `{Dir}/index`; the admin's is `{root}/ai`) and reports
  usage on `sys.ai.>` (records 30 days locally, heartbeat 60 s, outbox
  retry) to the admin collectors and the AI > Usage page. Never write AI
  sessions or notes into `admin.db`. Hosts without the vault use
  `ai.StaticCredentials`.
- `docsite/` serves a product's built Starlight documentation from an
  embedded `fs.FS` behind a session cookie the SPA sets with the user's
  bearer (`POST {prefix}/_session`; a page without one is redirected to
  the SPA's gate route, an asset answers 401) and reads the pages into
  sections with a keyword index; `ai/docsearch` wraps a site as an
  assistant corpus (`{prefix}docs_search`, `{prefix}docs_read`, prompt
  guidance, optional vector generations keyed by embedding profile under
  its own directory, embedded with the corpus's credential, never the
  caller's) handed to `instance.Options.Docs`. The Starlight side
  (`docsSite`, base-link rewriting, theme) and the browser side
  (`openDocsSession`, `DocsGate`) are in `@bboxsys/ui`; the product owns
  content, mount path, access policy and build steps. `docs/DOCSITE.md`
  is the reference.
- `job/` and `worker/` are the async job bus over NATS: `job.New` +
  `Dispatch` (to `job.request.*`, for a queue relay) or `Enqueue` (straight
  to `job.queue.{service}.{name}`), `worker.New(...).Run(handler)` takes
  jobs in queue group `workers` and reports `job.status.{id}`. `queue/` is
  the optional relay service (`queue.NewQueueServer`): it stores every
  request and latest status in `queue.db` (`Metadata["storeLocation"]`),
  forwards requests to the workers, and serves `GET/POST /api/v1/queue`.
  All three stay product-free. Tests run against an embedded NATS server
  via `messenger.NewClientFromConn`.
- `ai/` is the assistant (loop, `Service`, providers, tools, rag, memory,
  MCP); `docs/AI.md` is the reference. `ai/...` imports `admin/client`,
  never root `admin`; root `admin` hosts it via `StartAI`, a product via
  `ai.NewService` + `Mount`. The SDKs stay in `ai/anthropic`, `ai/mcp`,
  `ai/rag`. Model providers: `ai/anthropic` (`anthropic`) and `ai/openai`
  (`ollama` and `openai_compat`, the OpenAI chat-completions dialect over
  `net/http`; Ollama, llama-server, vLLM all serve it). An `ollama`
  endpoint is the server root (`ai.OllamaBaseURL`), an `openai_compat`
  endpoint the `/v1` base URL. `ai/instance` registers all three by
  default.
- AI history is append-only: `Message.Content` is the provider's native
  JSON and is never edited (models bind reasoning to it). Context pressure
  is the provider's server-side compaction, not trimming; a session is
  bound to its provider and model.
- AI credentials are API tokens with `resource_type` `ai_model`,
  `ai_embedding`, or `mcp_server` and `ai_*` tags (plus
  `mcp_trust_read_only`; remote MCP tools are writes without it). The constants live in
  `ai/credential.go` and `ui/src/lib/aiCredential.ts`; change both together.
- `ServiceOptions.AllowWrites` approves every non-read-only tool;
  `AllowTools` names the few whose writes stay in the caller's own scope
  (`memory`, `remember`) so memory works with writes off. Never list a
  tool there that reaches tenant records.
- API tools (`ai/apitool`) dispatch a synthetic request through the
  service's own handler with the caller's bearer, as natsapi does. Never
  add a tool-only path to a store. Every instance's tool calls go through
  `apitool.Bounded`: a response over `MaxToolResponseBytes` (32 KiB) is a
  complete 413 problem document naming a compact query (`ToolGuidance`),
  never a truncated list, and an identical failed GET reaches the handler
  at most twice per run. A service without an OpenAPI document derives
  one from its router with `instance.RouteTools` (plus `apicontract`
  documents for its compact queries); the returned filter is its
  `ToolFilter`, so `api_request` stays on the same surface.
- Service assistants reached through the console: `ai/instance/natshost`
  serves an instance over natsapi with request ids, cancellation and an
  orderly close; `admin.AssistantGateway` (`MountAssistantGateway`,
  `Register`) lists them at `/api/v1/ai/services` and relays the
  conversation routes on a connection inside the caller's account.
  `docs/AI.md` section 17. Products register; they do not patch the
  console or copy the host. With a service registered the gateway also hosts `all`
  (`admin/assistant_all.go`): a coordinator with one read-only `ask_{id}`
  tool per assistant that sends a self-contained question over the relay
  as the caller and returns the specialist's answer with its `session_id`;
  it has no API tools of its own and every specialist keeps its own write
  policy. Instance hosts add non-API tools through
  `instance.Options.Tools`. The kit owns the conversation UI
  (`createAssistantClient`, `AssistantChat`, `AssistantDrawer`,
  `AssistantMarkdown`); the console's Assistant page and a product's
  toolbar drawer are both built from it, never a copy. The admin keeps credential-minting and
  sign-in routes (`aiDeniedRoutes`, `Auth`-tagged operations) out of the
  tools and `admin_api_request`; `/mcp` (`?accountID=`, since MCP clients
  cannot send the account header) serves the same set filtered to
  read-only tools plus `memory` and `remember` unless `ai.allow_writes`.

## Build & test

```
go build ./...
go test ./admin/ ./admin/client/ ./user/ ./server/ ./config/ ./logger/ ./agent/ ./job/ ./worker/ ./queue/ ./natsapi/ ./docsite/ ./ai/...
cd ui && npm run typecheck && npm test   # shared Vue kit
cd admin/ui && npm run build        # admin SPA (needed before -tags adminui)
go build -tags adminui ./...        # verify the embed
```

Local dev needs a repo-local `go.work` (gitignored) that uses `.` and
`../store`; it shadows the shared `~/go/src/github.com/go.work`, whose
vendor tree goes stale and breaks `go run` with "inconsistent vendoring".
Create it with `go work init . ../store` if it is missing.

`./run.sh` runs `cmd/admin` against `~/.config/platform-test/`
(seeded by the idempotent `admin -init` on every run, port 8901, includes a
dev root + intermediate CA for agent registration, and an embedded NATS
controller on 8902 with monitoring on 8903 so the Services, Logs, and
Store metrics pages have a source, and a queue relay service on 8904 that
feeds the Queue page; `controller.embedded = true` and `queue_server.port`
are what turn them on in a config); `./run-agent.sh`
registers a host agent with it as the default super admin and runs it from
`~/.config/agent-test/`; `cd admin/ui && npm run dev` serves the
SPA on :4400 and proxies the API to it. `ADMIN_PROXY_TARGET_DEV` moves that
proxy to another server; `BBOX_DEV_ENDPOINTS` rewrites the service URLs the
map publishes (it, not the proxy, decides where a SPA signs in). Both come
from `@bboxsys/ui/vite` and work in every consumer SPA -- see
`docs/DEVELOPER.md` section 11.

## Release

Apache-2.0, public module. Tag `v0.x.y` on main; the tag also publishes
`@bboxsys/ui` at that version to npm (secret `NPM_TOKEN`). Consumers update Go with
`GOWORK=off go get github.com/bboxsys/platform@v0.x.y` and the SPA with
`npm install @bboxsys/ui@^0.x.y`; neither needs a token.
Consumers develop against this checkout through their own repo-local
`go.work`, so no vendor refresh is needed after a bump.
