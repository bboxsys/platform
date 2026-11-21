# admin UI

Vue 3 single-page app for the admin service: accounts, users, invitations,
service and agent identities, PKI, NATS service status, logs, store metrics,
profile, and API tokens. Built with shadcn-vue (Reka UI + Tailwind v4),
Pinia, and vue-router.

The app is served by the admin server on the same origin as its API, so
there is no CORS, no service discovery, and no separate deployment. The Go
binary embeds `dist/` when built with `-tags adminui`.

## Shared kit

Login, the session store, the admin API types, and the generic components
come from `@bboxsys/ui`, which is the sibling directory `../../ui` linked by
path (`"file:../../ui"` in package.json). Edits there show up here on the
next Vite reload; `npm run build` type-checks the kit source too.

## Develop

```
../../run.sh                       # admin server on https://localhost:8901
npm install
npm run dev                        # http://localhost:4400
```

The Vite dev server proxies `/api`, `/serviceendpoints`, and `/jwt` to a
running admin server so the SPA behaves exactly as it does embedded. The
default target is the `run.sh` test profile on port 8901; set
`ADMIN_PROXY_TARGET=https://localhost:8900` to develop against another
running admin server instead.

## Build and embed

```
npm run build                      # writes dist/
cd .. && go build -tags adminui ./cmd/admin
```

## Layout

```
src/
  api/admin.ts        typed client for every admin route
  types/admin.ts      TypeScript transcriptions of the Go structs
  stores/session.ts   login, token, super-admin flag (persisted in localStorage)
  router.ts           routes and guards (public, super-admin, tenant-scoped)
  views/              one file per screen; account/ holds the account tabs
  components/         DataTable, ConfirmDialog, PageHeader, StatusBadge
  components/ui/      shadcn-vue components (generated; edit freely)
  lib/http.ts         fetch wrapper with auth headers and the in-flight counter
```

Auth model: bearer tokens are NATS user JWTs from `/api/v1/login`; every
request also carries `X-Blackbox-AccountID`. `is_super_admin` means the user
is in the ADMIN account; tenant administrators are confined to their own
account by the router guard and by the server.
