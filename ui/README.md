# @bboxsys/ui

Shared Vue 3 UI kit for apps built on the platform: the theme
(one token stylesheet with light and dark, a canvas color bridge, chart and
tone helpers, the mode toggle), the session store and login card that sign
in through the platform admin server, the admin API types, the HTTP client with the in-flight request counter,
DataTable and ObjectTable (the standard table toolbar, with ObjectAction and
ClearableSearchInput), MetricTile, AccentSurface, AppNotice, TermList,
ConfirmDialog, PageHeader, StatusBadge, the Leaflet GeoMap, the
OpenAPI ApiExplorer, the API token authentication form and helpers (static
bearer or backend-minted OIDC), formatting and geo helpers, and a
self-contained copy of the shadcn-vue primitives they use.

The package ships raw `.vue`/`.ts` source; the consuming Vite app compiles
it with its own Vue and Tailwind v4 toolchain. It lives in the
`bboxsys/platform` repository beside the Go packages and the admin SPA and
is published to npm as `@bboxsys/ui` on every platform release.

## Install

The package is public on npm; no registry configuration or token is
needed:

```
npm install @bboxsys/ui@^0.10.0
```

## Consumer setup

1. Tailwind must scan the package. In the app's main CSS (after the tailwind
   import):

   ```css
   @source "../node_modules/@bboxsys/ui/src";
   ```

2. Import the theme after the shadcn base stylesheet, then a brand preset
   (`blue`, the default, or `orange`). The theme defines every CSS variable
   the components read, the Tailwind utilities over them, and both light and
   dark; the preset sets the brand the chrome derives from:

   ```css
   @import "@bboxsys/ui/theme.css";
   @import "@bboxsys/ui/brands/blue.css";
   ```

3. Import from the package root:

   ```ts
   import { createSessionStore, LoginCard, DataTable, request } from '@bboxsys/ui'
   ```

## Dev proxy

Apps proxy their API to a running backend in development so the SPA stays
same-origin, exactly as it is when embedded in the binary. `@bboxsys/ui/vite`
builds that proxy map and honours `ADMIN_PROXY_TARGET_DEV`, so one variable
repoints every app that uses it:

```ts
// vite.config.ts
import { devProxy } from '@bboxsys/ui/vite'

export default defineConfig({
  server: {
    port: 4320,
    proxy: devProxy(['/serviceendpoints'], {
      fallback: 'https://localhost:8880',
      env: 'MYAPP_UI_SERVER',
    }),
  },
})
```

The target is resolved in order: `ADMIN_PROXY_TARGET_DEV`, then the app's own
variable if `env` names one, then `fallback`. So an app keeps whatever
variable it already documented, and a developer running several SPAs against
one backend sets the shared one instead:

```
ADMIN_PROXY_TARGET_DEV=https://localhost:9900 npm run dev
```

`devProxyTarget(opts)` returns just the resolved URL for configs that need it
elsewhere. Only the listed path prefixes are proxied: an app that discovers
absolute service URLs from `/serviceendpoints/` still calls those directly,
and they stay subject to that service's CORS configuration.

### Overriding the service endpoint map

Moving the proxy does not change where the SPA signs in. Apps discover service
URLs from the server's `/serviceendpoints/` map, and the URLs inside that map
win: point the proxy at another backend and the login card still shows
whatever `login_service` the map published. `devEndpoints()` rewrites entries
of the map for the dev server only, so the browser code stays identical to
production:

```ts
import { devEndpoints, devProxy } from '@bboxsys/ui/vite'

const devServer = { fallback: 'https://localhost:8880', env: 'MYAPP_UI_SERVER', project: 'myapp' }

export default defineConfig({
  plugins: [vue(), devEndpoints(devServer)],
  server: { proxy: devProxy(['/serviceendpoints'], devServer) },
})
```

Then:

```
BBOX_DEV_ENDPOINTS=https://localhost:9900 npm run dev
```

A bare URL is shorthand for the admin/login pair (`login_service` and
`admin_service`). Any kind can be set explicitly, and unlisted kinds keep the
URL the server published:

```
BBOX_DEV_ENDPOINTS=login_service=https://localhost:9900,aws_service=http://localhost:9910
```

With `project` set, the plugin also reads
`~/.config/{project}-test/dev-endpoints.json` for a setup worth keeping
between sessions, alongside whatever else lives in that directory:

```json
{ "login_service": "https://localhost:9900", "admin_service": "https://localhost:9900" }
```

The environment variable wins over the file. With neither set the plugin
stands aside and the route is proxied untouched. It prints the overrides it
applied at startup, so a dev server that is not using them says so by
staying quiet.

## Develop

```
npm install
npm run typecheck
npm test
```

To exercise a change in a real app, run the platform admin server with
`../run.sh` and the admin SPA with `cd ../admin/ui && npm run dev`; the SPA
depends on this directory by path. Other apps can link it temporarily with
`npm install --no-save ../../platform/ui`.

## Documentation sites

A product's Starlight documentation site configures itself with
`@bboxsys/ui/starlight` and themes itself with `@bboxsys/ui/starlight.css`;
the SPA opens it through `DocsGate` and `openDocsSession`. The platform's
`docs/DOCSITE.md` describes the whole contract, including the Go handler
that serves the site behind sign-in and the assistant tools over it.

## Release

Tag `v0.x.y` on platform `main`. The Release workflow publishes this
package at that version; consumers bump `@bboxsys/ui` in package.json and
run `npm install`.
