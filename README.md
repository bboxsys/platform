# platform

[![Go Reference](https://pkg.go.dev/badge/github.com/bboxsys/platform.svg)](https://pkg.go.dev/github.com/bboxsys/platform)
[![npm](https://img.shields.io/npm/v/@bboxsys/ui?label=%40bboxsys%2Fui)](https://www.npmjs.com/package/@bboxsys/ui)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

A multi-tenant control plane for Go services, and the packages those
services are built on.

The admin server is the identity provider: tenant accounts, users, roles,
invitations, API tokens, OAuth sign-in, a private PKI, and a registry of
service endpoints. Services validate its tokens locally, talk to each other
over [NATS](https://nats.io), and share one web console and UI kit.

```sh
go get github.com/bboxsys/platform
```

## What's inside

| Area | Packages | What you get |
|---|---|---|
| **Identity** | `admin`, `admin/client`, `user`, `oidctoken` | The admin server and its store; `Auth` middleware that validates tokens with no call back to the admin |
| **Service base** | `server`, `config`, `logger`, `supervisor` | HTTPS server with TLS, Let's Encrypt and CORS; configuration loading; structured logging; process composition |
| **Messaging** | `messenger`, `controller`, `natsapi` | NATS client, an embedded NATS server, and the HTTP API bridged over NATS with the same routes and permissions |
| **Jobs** | `job`, `worker`, `queue` | Background jobs over NATS: dispatch, queue-group workers, and a relay service with history |
| **AI assistant** | `ai/...` | Agent loop and HTTP service, Anthropic and OpenAI-compatible providers, tools from a service's OpenAPI document, retrieval, memory, MCP |
| **Docs** | `docsite` | Serve a product's documentation site behind sign-in and make it searchable by the assistant |
| **Hosts** | `agent`, `host`, `pki` | A headless host agent that registers with the admin, plus the X.509 CA that issues its certificates |
| **Web** | `admin/ui`, `ui` | The admin console (Vue), and [`@bboxsys/ui`](ui/README.md), the UI kit product consoles share |

## Authenticate a service

A service that only needs to check who is calling imports `admin/client`
and nothing else:

```go
import admin "github.com/bboxsys/platform/admin/client"

auth := admin.NewAuth(admin.AuthConfig{
    Logger:          logger,
    TrustedAccounts: trustedAccountIDs, // account public keys issued by the admin
})
router.HandleFunc("/api/v1/things", auth.Bearer(handler))

func handler(w http.ResponseWriter, r *http.Request) {
    accountID := admin.AccountIDFromContext(r.Context())
    userID := admin.UserIDFromContext(r.Context())
}
```

Tokens are NATS user JWTs signed by an account key. `Auth` verifies the
signature and checks that the issuing account is trusted, so there is no
shared secret and no network call per request.

## Host the admin server

Run it inside your own process, or standalone as `cmd/admin`:

```go
adminServer, err := admin.NewAdminServer(&server.ServerOptions{
    Component: "admin_server",
    HTTPPort:  8443,
    // TLS material, store location, SMTP and OAuth settings via Metadata
})
go adminServer.Start()
```

## Run it locally

```sh
./run.sh                        # admin API on :8901, with an embedded NATS server and job queue
cd admin/ui && npm install && npm run dev   # admin console on http://localhost:4400
./run-agent.sh                  # optional: register and run a host agent against it
```

The first run seeds a test profile under `~/.config/platform-test/` with a
config file, self-signed certificates and a development CA.

## Documentation

| Guide | Covers |
|---|---|
| [Developer guide](docs/DEVELOPER.md) | Every package, with integration recipes: authentication, hosting the admin, configuration, jobs, the API over NATS |
| [AI assistant](docs/AI.md) | Providers, tools, retrieval, memory, MCP, and hosting an assistant in a service |
| [Documentation sites](docs/DOCSITE.md) | Embedding a Starlight site behind sign-in |
| [OIDC API tokens](docs/OIDC_API_TOKENS.md) | API tokens that mint and renew their bearer through OIDC |
| [UI kit](ui/README.md) | `@bboxsys/ui`, the Vue kit for product consoles |

## Development

```sh
go build ./...
go test ./admin/ ./admin/client/ ./user/ ./server/ ./config/ ./logger/ ./agent/ ./job/ ./worker/ ./queue/ ./natsapi/ ./docsite/ ./ai/...
cd ui && npm install && npm run typecheck && npm test
```

Requires Go 1.26 and Node.js 22. Persistence is
[bboxsys/store](https://github.com/bboxsys/store), a JSON document store
on BadgerDB.

## License

[Apache-2.0](LICENSE). The source is published for the projects that build
on it. It is maintained by Black Box Systems and is not seeking outside
contributions.
