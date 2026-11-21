# platform

The Black Box Systems control plane: identity provider, tenant accounts, roles,
invitations, API tokens, OAuth, PKI, service endpoint registry, and the log and
store-metrics sinks. This module also carries the platform packages its
services are built on (`server`, `config`, `logger`,
`messenger`, `pki`, `user`).

```
go get github.com/bboxsys/platform
```

Licensed under the Apache License 2.0 (`LICENSE`). The source is published
for the projects that build on it; it is maintained by Black Box Systems
and is not seeking outside contributions.

## Packages

| Package | Purpose | Who imports it |
|---|---|---|
| `github.com/bboxsys/platform/admin` | The admin server: `NewAdminServer`, `AdminServer`, `Store`, `LogStore`, `MetricsStore` | The process that hosts the admin service |
| `github.com/bboxsys/platform/admin/client` | `Auth` and `ID` middleware, `AccountIDFromContext`/`UserIDFromContext`, tenant NATS subjects and notifications, `ServiceEndpoint` | Every service that validates tokens issued by admin |
| `user` | Identity model: users, accounts, services, agents, roles, credentials, JWT claims | Services and the admin server |
| `server` | Base HTTPS server with TLS, Let's Encrypt, CORS, and NATS wiring | Every HTTP service |
| `config` | Viper-based configuration loader and template generation | Service entry points |
| `logger` | zerolog wrapper with request logging and NATS log hook | Everything |
| `messenger` | NATS client | Services that publish or subscribe |
| `job`, `worker` | Asynchronous jobs over NATS: job requests, status updates, queue-group workers | Services that run or request background work |
| `queue` | Job relay service: stores requests and statuses, forwards to workers, REST submit/list/get | The process that hosts a queue server |
| `natsapi` | The HTTP API over NATS request/reply: same routes and permissions, chunked streaming, micro discovery, `http.RoundTripper` client | Services that serve agents and AI callers over NATS; agents calling them |
| `ai`, `ai/anthropic`, `ai/apitool`, `ai/rag`, `ai/memory`, `ai/mcp` | AI assistant: agent loop and HTTP service, Anthropic provider, tools from the OpenAPI document, retrieval, memory, MCP server and client (docs/AI.md) | The admin server (`StartAI`) and product servers that host an assistant |
| `pki` | X.509 CA, intermediate, certificate issuance, OS trust store | admin server, host agent |
| `storemetrics` | Store metrics entry model and periodic publisher | Service entry points |
| `logentry` | Log entry model and NATS subjects | logger, admin server |
| `letsencrypt`, `clientip`, `utils` | Support packages for `server`, `logger`, and `pki` | Indirect |

`client` is the API boundary. A service that only needs to authenticate
requests imports `client` and never touches the admin server, the BadgerDB
stores, or the UI:

```go
import admin "github.com/bboxsys/platform/admin/client"

auth := admin.NewAuth(admin.AuthConfig{
    Logger:          logger,
    TrustedAccounts: trustedAccountIDs, // account public keys issued by admin
})
router.HandleFunc("/api/v1/things", auth.Bearer(handler))

func handler(w http.ResponseWriter, r *http.Request) {
    accountID := admin.AccountIDFromContext(r.Context())
    userID := admin.UserIDFromContext(r.Context())
}
```

Token validation is self-contained: bearer tokens are NATS user JWTs signed
by an account nkey, and `Auth` verifies the signature and checks the issuing
account is in its trusted list. No shared secret and no call back to the
admin server are needed. `client.IdentityStore` is an optional interface for
services that also need basic auth or role checks; `*admin.Store` satisfies
it.

## Hosting the admin service

The admin server runs inside a product's own process, or on its own as
`cmd/admin`. It listens on its own TLS port and the SPA discovers
it through the `/serviceendpoints/` map served by the UI server.

```go
adminServer, err := admin.NewAdminServer(&server.ServerOptions{
    Component: "admin_server",
    HTTPPort:  8443,
    // TLS material, store location, SMTP and OAuth settings via Metadata
})
adminServer.StartNATSAPI(admin.NATSAPIOptions{}) // also serve the API over NATS as admin_service
go adminServer.Start()
```

## Calling the API over NATS

Any service built on `server` can serve the same HTTP API over the NATS
controller (`ServerOptions.NATSAPI`), and the admin serves `admin_service`
in every account. A host agent or an AI service that knows only the
controller and holds its own credentials calls it with the same verbs,
paths, payloads, and permissions as over HTTPS:

```go
client := natsapi.NewClient(nc)                                   // *http.Client
req, _ := natsapi.NewRequest(ctx, "GET", "admin_service", "/api/v1/users", nil)
req.Header.Set("Authorization", "Bearer "+cfg.JWT)               // the caller's own NATS user JWT
resp, err := client.Do(req)
```

From a registered host: `agent api GET /api/v1/users`, `agent api POST
/api/v1/users --data @user.json`, `agent services`. Bodies stream in chunks,
so the streaming list endpoints stay streaming, and every served
connection answers nats micro discovery (`$SRV.INFO`). Section 12 of the
developer guide has the wire format, multi-tenant serving, and how to scope
an agent's JWT to read-only access.

## Development

```
go build ./...
go test ./admin/ ./admin/client/ ./user/ ./server/ ./config/ ./logger/ ./agent/ ./job/ ./worker/ ./queue/ ./natsapi/ ./ai/...
```

Full API reference and integration recipes: [docs/DEVELOPER.md](docs/DEVELOPER.md).
Renewable OIDC API tokens (storage contract, resolver, kit form):
[docs/OIDC_API_TOKENS.md](docs/OIDC_API_TOKENS.md).

## Layout

```
admin/           admin server package (handlers, store, oauth, pki, proxy)
admin/client/    middleware and context helpers for other services
admin/email/     invitation email senders (SMTP, noop)
admin/ui/        Vue admin SPA (embedded with -tags adminui)
cmd/admin/       standalone admin binary
agent/           headless host agent CLI (register + run), branded per product
cmd/agent/       unbranded agent binary (./run-agent.sh registers and runs it locally)
supervisor/      routine supervisor used by service entry points
controller/      embedded NATS server
host/            host facts and the on-host agent runtime (SR-IOV PF/VF)
job/ worker/      asynchronous jobs over NATS (requests, status, queue-group workers)
queue/           job relay service with history store and REST surface
natsapi/         the HTTP API bridged over NATS (service side and client transport)
ai/              AI assistant: loop, service, Anthropic provider, API tools, rag, memory, MCP
user/ server/ config/ logger/ messenger/ pki/ storemetrics/ logentry/
letsencrypt/ clientip/ utils/
```
