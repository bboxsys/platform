# @bboxsys/ui Developer Reference

Everything a consuming app (or a developer working in one) needs to use
this kit. The package ships raw `.vue`/`.ts` source (`main: ./src/index.ts`,
no build step); the consuming Vite app compiles it with its own Vue,
TypeScript, and Tailwind v4 toolchain.

The kit lives in `bboxsys/platform/ui` and is published as `@bboxsys/ui`
to the public npm registry on every platform release. Consumers:
the platform's own admin SPA (`admin/ui`, by path) and every product SPA
built on the platform.

## Contents

- [Install and consumer contract](#install-and-consumer-contract)
- [Release process](#release-process)
- [Dev server helpers (@bboxsys/ui/vite)](#dev-server-helpers)
- [Theme (@bboxsys/ui/theme.css, tokens, useTheme, ThemeToggle)](#theme)
- [Session store (createSessionStore)](#session-store)
- [LoginCard](#logincard)
- [ServiceApiExplorer](#serviceapiexplorer)
- [Admin API types (types/admin)](#admin-api-types)
- [API token authentication (lib/apitokenAuth, ApiTokenAuthFields)](#api-token-authentication)
- [AI credentials (lib/aiCredential)](#ai-credentials)
- [HTTP client (lib/http)](#http-client)
- [useAsync and errorMessage](#useasync)
- [usePolling and usePagedList](#usepolling-and-usepagedlist)
- [Formatting helpers (lib/format)](#formatting-helpers)
- [Geo helpers (lib/geo)](#geo-helpers)
- [DataTable](#datatable)
- [ObjectTable](#objecttable)
- [Page scroll focus (focusPageScroll)](#page-scroll-focus-focuspagescroll)
- [ObjectAction / ClearableSearchInput](#objectaction--clearablesearchinput)
- [ConfirmDialog](#confirmdialog)
- [PageHeader / StatusBadge](#pageheader--statusbadge)
- [PageTabs](#pagetabs)
- [AccentSurface / MetricTile / TermList](#accentsurface--metrictile--termlist)
- [AppNotice / AppNoticeBand](#appnotice--appnoticeband)
- [GeoMap](#geomap)
- [ApiExplorer](#apiexplorer)
- [Assistant (createAssistantClient, AssistantChat, AssistantDrawer, AssistantMarkdown)](#assistant)
- [Documentation site and gate (docsSite, DocsGate, openDocsSession)](#documentation-site-and-gate)
- [Vendored shadcn primitives (src/ui)](#vendored-shadcn-primitives)

## Install and consumer contract

The package is public on npm. An app installs it like any other
dependency; no `.npmrc` scope mapping and no token are needed, locally or
in CI.

```json
"dependencies": { "@bboxsys/ui": "^0.5.0" }
```

Inside the platform repository the admin SPA depends on the kit by path
(`"file:../../ui"`) and never touches the registry. Its Vite config and
tsconfig set `preserveSymlinks` (plus `resolve.dedupe` for the shared
runtimes) so the linked source resolves Vue, Pinia, and reka-ui from the
app and the kit needs no node_modules of its own. For the same reason this
package's `tsconfig.json` must stay self-contained and never `extends` a
package.

Required app-side setup:

1. Tailwind must scan the package source. In the app's main CSS, after the
   tailwind import:
   ```css
   @source "../node_modules/@bboxsys/ui/src";
   ```
   (the admin SPA points at `../../../ui/src` directly because its copy is
   a symlink.)
2. All runtime deps are peerDependencies -- the app installs them itself:
   `vue`, `pinia`, `@vueuse/core`, `@lucide/vue`, `reka-ui`, `clsx`,
   `class-variance-authority`, `tailwind-merge`, `marked`, `dompurify`,
   `echarts`; plus `leaflet` (optional peer). Because the app's `vue-tsc`
   type-checks this package's source, any app that depends on the kit ALSO
   needs `@types/leaflet` in devDependencies (GeoMap has a type-only
   leaflet import) even if it never renders a map. Apps that do render maps
   need `leaflet` in dependencies and `@import "leaflet/dist/leaflet.css";`
   in their CSS. `marked`, `dompurify` and `echarts` (since 0.19.0) are
   resolved at type-check time by the assistant components but loaded only
   when a reply is rendered or a chart drawn, so an app that never opens
   the assistant ships none of them.
3. The app imports the kit's theme stylesheet after the shadcn base:
   ```css
   @import "@bboxsys/ui/theme.css";
   ```
   It defines every shared CSS variable the components style themselves
   with (`--background`, `--primary`, `--border`, `--popover`, ...), the
   brand, status and chart tokens, the Tailwind color utilities over them,
   the `dark` variant and both themes. See [Theme](#theme).
4. Import ONLY from the package root: `import { DataTable } from '@bboxsys/ui'`.
   The exceptions are the non-TypeScript entry points:
   `@bboxsys/ui/vite` (dev server helpers), `@bboxsys/ui/theme.css` and the
   `@bboxsys/ui/brands/*.css` presets.

## Release process

The kit is released with the platform Go module, under the same version:

1. Merge to platform `main`.
2. Tag `v<version>` and push the tag. `.github/workflows/release.yml`
   typechecks and tests the kit, sets its package version from the tag, and
   runs `npm publish` to npm with the `NPM_TOKEN` repository secret.
3. In each consumer, bump `@bboxsys/ui` in package.json, run
   `npm install`, and build.

`version` in `ui/package.json` is informational between releases; the tag
is authoritative. Keep changes backward compatible within a minor line.

## Dev server helpers

`@bboxsys/ui/vite` is the kit's second entry point, imported from an app's
`vite.config.ts`. It is plain ESM JavaScript (Node loads the config, not the
bundler) with types in `src/vite/devProxy.d.mts`.

```ts
import { devEndpoints, devProxy } from '@bboxsys/ui/vite'

const devServer = { fallback: 'https://localhost:8880', env: 'MYAPP_UI_SERVER', project: 'myapp' }

export default defineConfig({
  plugins: [vue(), devEndpoints(devServer)],
  server: { proxy: devProxy(['/serviceendpoints'], devServer) },
})
```

`devProxy(paths, opts)` returns a `server.proxy` map pointing each path
prefix at the resolved target, with `secure: false` for self-signed dev
certificates. `devProxyTarget(opts)` returns just the URL. The target is
`ADMIN_PROXY_TARGET_DEV`, then `opts.env` if the app names its own variable,
then `opts.fallback`.

`devEndpoints(opts)` is a serve-only plugin that answers `/serviceendpoints/`
from the dev server instead of proxying it: it fetches the real map from the
same target and applies overrides on top. This is the only way to change
where a SPA signs in without editing server config, because the proxy target
and the URLs published *inside* the map are different things -- a SPA reads
`login_service` from the map, so moving the proxy alone changes nothing.

```
BBOX_DEV_ENDPOINTS=https://localhost:9900 npm run dev
BBOX_DEV_ENDPOINTS=login_service=https://localhost:9900,aws_service=http://localhost:9910 npm run dev
```

`watchLinkedKit(opts)` is a serve-only plugin for an app that depends on
the kit by path (`file:../../ui`, as the admin SPA does, or a consumer's
local link). The kit is then a symlink under `node_modules`, which Vite's
watcher ignores and does not follow, so without the plugin a running dev
server keeps serving the kit as it was at startup: a new export fails to
import and a lazy route that needs it silently does not load. The plugin
watches the link's real `src`; on a change it stamps the module and every
importer up to the entry with a new timestamp and reloads the page. The
stamp matters: Vite serves `node_modules` files under a `?v=` version with
an immutable cache header, so without a new URL the browser would keep the
old module even after a reload. It is a no-op for an installed release.

```ts
plugins: [vue(), devEndpoints(devServer), watchLinkedKit()]
```

A bare URL is shorthand for `login_service` + `admin_service`. Unlisted
kinds keep the URL the server published. With `project` set the plugin also
reads `~/.config/{project}-test/dev-endpoints.json`
(`{"login_service": "..."}`); the environment variable wins over the file.
With neither configured the plugin stands aside and the route is proxied
untouched. Overrides in effect are printed at startup.

Those helpers are dev-only: `devProxy` feeds `server.proxy`, and
`devEndpoints` and `watchLinkedKit` set `apply: 'serve'`. Nothing reaches
a production build, and the SPA's runtime code is identical either way.

`brandTokens(source?)` is the one build-time plugin. A product whose brand
is not a kit preset keeps it in `src/brand.css` (see Branding an app); the
plugin reads that file at build time, keeps only the custom property
declarations of its `:root` and `.dark` blocks (comments, `@import`s, font
faces and `url()` values are left behind, since they would resolve against
the wrong origin elsewhere), and writes them to `brand-tokens.css` beside
`index.html` in `dist/`. The product already embeds `dist/`, so the admin
console it hosts reads its brand from there through `admin.HostBranding`
with no second copy in Go. The build fails, naming the path, when the file
is missing or sets no brand.

```ts
import { brandTokens, devEndpoints, devProxy } from '@bboxsys/ui/vite'

plugins: [vue(), tailwindcss(), devEndpoints(devServer), brandTokens()]
```

`extractBrandTokens(css)`, `starlightBrandCSS(css)` (the same tokens
mapped onto Starlight's two themes, used by `docsSite({ brand })`) and
`readBrandTokens(file, root?)` are exported for tests and tooling.

## Theme

The kit owns the palette. `src/theme/theme.css` is the single definition of
every shared color, published as `@bboxsys/ui/theme.css`, and `src/theme/`
holds the code that reads it. An app's own `style.css` shrinks to the font
import, the tailwind and shadcn imports, the `@source` line, the theme import,
and whatever tokens are that product's alone:

```css
@import url('https://fonts.googleapis.com/css2?family=Geist:wght@400;500;600;700&display=swap');

@import "tailwindcss";
@import "tw-animate-css";
@source "../node_modules/@bboxsys/ui/src";
@import "shadcn-vue/tailwind.css";
@import "@bboxsys/ui/theme.css";
```

The stylesheet carries the `dark` custom variant, the `:root` (light) and
`.dark` token sets, a `@theme inline` block that turns every token into a
Tailwind utility (`bg-brand`, `text-status-warn-text`, `border-sidebar-border`,
`bg-chart-3`), the base layer (`border-border`, body background and font),
and the two renderer hooks every app had pasted: `.themed-tooltip` for echarts
and the dimmed `.dark .leaflet-tile`. Tailwind v4 processes `@theme` and
`@custom-variant` from an imported package stylesheet, so nothing about the
import is special.

### Tiers

A color speaks for exactly one of these; if a color could mean two things it
means nothing.

| Tier | Tokens | Says | Themed |
| --- | --- | --- | --- |
| Brand | `--brand`, `--brand-highlight`, `--brand-hue` | what a thing is, how things group, what is interactive | yes |
| Status | `--status-good` / `-warn` / `-bad` (+ `-text`) | what state a thing is in | fills no, text yes |
| Series | `--chart-1` .. `--chart-7` | which entity a mark belongs to | fills only |

`--primary`, `--ring`, `--sidebar-primary` and the accent washes are derived
from `--brand`, so the brand drives the chrome. The status FILLS are identical
in both themes because a permit/deny reading has to stay green/amber/red and
must not shift when a brand palette does; the `-text` variants are themed
because the fills are too light to read on a light surface. Status color
never carries meaning alone: ship it with a label, an icon or a number.

### Branding an app

Every surface, border and muted text is tinted toward `--brand-hue` at chroma
under 0.02 (below the point where a hue is nameable), so cards and sidebars
sit under the brand rather than beside it. An app picks its brand by importing
a preset after the theme; the chrome, the utilities and the canvas bridge all
follow:

```css
@import "@bboxsys/ui/theme.css";
@import "@bboxsys/ui/brands/orange.css";
```

| Preset | Brand | Highlight |
| --- | --- | --- |
| `brands/blue.css` (the default) | azure blue | cyan |
| `brands/orange.css` | orange | blue |

The platform admin console imports no preset: it is one build served by every
host, so it takes its brand from the host's `GET /branding/` bootstrap
(`branding_brand` names a preset, `branding_css` carries a non-preset token
block) and applies it at runtime, setting `data-brand` on `<html>`. The canvas
bridge treats that attribute like the `dark` class and re-resolves its tokens.

Both carry the identity of the app's earlier Angular Material theme (azure
plus cyan tertiary, orange plus blue tertiary). A preset sets only the five
brand tokens for both themes -- `--brand-hue`, `--brand`, `--brand-foreground`,
`--brand-highlight`, `--brand-highlight-ink` -- and a test asserts every preset
sets all of them. An app whose brand is not a preset writes the same block
itself (a product's own brand lives in that product, not here), and may go further and
override chrome tokens such as `--background` or `--muted-foreground` below the
import when the brand prescribes them.

Write that block once, in `src/brand.css`, and import it from the app's
stylesheet after the theme. The brand then reaches the two surfaces the app
does not build itself from the same file: `brandTokens()` (Dev server
helpers) copies its token blocks into `dist/brand-tokens.css` for the
admin console the app embeds, which the Go host hands over with
`admin.HostBranding{Name, LogoSVG, UI: app.UIFS()}.Metadata()`, and
`docsSite({ brand: '../app/src/brand.css' })` maps them onto the
documentation site's themes. Only `:root` and `.dark` custom properties
travel; font faces and the `@theme inline` block stay with the SPA.

```css
/* src/style.css */
@import url('https://fonts.googleapis.com/css2?family=Geist:wght@400;500;600;700&display=swap');
@import "tailwindcss";
@import "@bboxsys/ui/theme.css";
@import "./brand.css";

/* src/brand.css */
:root {
  --brand-hue: 150;
  --brand: oklch(0.55 0.17 var(--brand-hue));
  --brand-highlight: oklch(0.78 0.15 90);
  --brand-highlight-ink: oklch(0.5 0.12 90);
}
.dark {
  --brand: oklch(0.65 0.16 var(--brand-hue));
  --brand-highlight-ink: oklch(0.82 0.12 90);
}
```

Keep every override in oklch, hex or rgb(): the canvas bridge parses those
three forms and passes anything else through as text.

The typeface is the app's: the `@import url()` that loads it must be the first
rule of the app's stylesheet, and the app then names it in its own
`@theme inline { --font-sans: ...; --font-heading: ...; }` block after the
theme import (the kit's default is Geist). `--font-heading` is what the kit's
card, dialog and sheet titles use.

Tokens a product adds for itself (entity identity colors for a graph, the
chrome of a custom diagram) go in the app's own `:root` / `.dark` blocks with a
second `@theme inline` block if they need utilities. They layer over the kit;
nothing in the kit needs to know about them.

### Canvas bridge (`token`, `palette`, `themeEpoch`)

DOM elements read the tokens through utilities or `var()`. echarts, cytoscape
and pixi paint into a bitmap and need a concrete color string, so the kit
resolves tokens for them:

```ts
import { palette, themeEpoch, token } from '@bboxsys/ui'

const stylesheet = () => [
  { selector: 'node', style: { 'background-color': token('--entity-service') } },
  { selector: 'edge.denied', style: { 'line-color': palette.value.bad } },
]
watch(themeEpoch, () => cy.style(stylesheet()))
```

`token(name, fallback?)` returns the resolved value of a custom property as a
hex string (`--` optional), or the fallback when the property is undefined --
which is the case in unit tests and on a first server-rendered paint.
Resolved values are cached per `<html>` class list, so a theme switch can
never serve a stale color regardless of watcher order. `palette` is a computed
with the common set (brand, status, series, ink, muted, grid, axis, surfaces)
that recomputes when `themeEpoch` bumps; watch `themeEpoch` from anything that
builds a chart or graph stylesheet. `colorToHex` and `oklchToHex` are exported
for tests and for code that receives a color from elsewhere.

A computed custom property is not the text written in the stylesheet: an
engine may return `oklch(55.3% .247 262)` for `oklch(0.553 0.247 262)`. The
bridge parses both, plus hex and `rgb()`.

### Chart helpers (`chartTheme`, `STATUS`, utilization bands)

`chartTheme()` returns `{ series, ink, muted, grid, axis, surface }` from the
tokens; the parameter older call sites pass is ignored. `baseTimeChartOption(t)`
is the shared axis/grid/tooltip scaffolding for a compact time-series chart
(its tooltip uses `.themed-tooltip`). `STATUS.good` / `.warning` /
`.critical` are the status fills. `utilizationBand(percent)` is `normal`
below 70, `warning` to 90, `critical` from 90 -- three bands, not four,
because two adjacent warning steps fall under the color-separation floor --
with `UTILIZATION_BAND_LABELS` and `utilizationColor(percent)` for a meter.
The series order is a CVD-safety mechanism: assign slots in the fixed order by
entity, never cycled or re-ranked.

### Tone maps (`TONE_DOT`, `TONE_TEXT`, `TONE_NOTICE`, `RAIL`)

`Tone` is `'default' | 'muted' | 'good' | 'warn' | 'bad'`. The maps resolve a
tone to utility classes over the status tokens -- a dot fill, text, and a
tinted notice (border, wash, text) -- so a dot, a banner and a chart series
read the same green. Use them instead of raw Tailwind palette classes
(`bg-emerald-500`, `text-amber-700 dark:text-amber-300`), which duplicate the
tokens by hand and cannot follow a theme change. `RAIL` / `RAIL_ICON` are the
left rail and wash for a metric tile, keyed by `Rail = 'brand' | 'highlight' |
Tone`: an app assigns the two brand rails to its own domains and a status tone
takes over whenever the tile carries a real reading. `RAIL_METER` is the fill
of a tile's meter at the same key. `ACCENT_SURFACE` is the frame and top rail
of an accent surface, keyed by `AccentTone = 'brand' | 'highlight' | 'chart-3'
| 'chart-5'`. `MetricTile`, `AccentSurface` and `AppNotice` are the components
over these maps; reach for the maps only where a component does not fit.

### Mode (`useTheme`, `ThemeToggle`)

```ts
const { mode, isDark, toggle } = useTheme()
mode.value = 'system'   // 'light' | 'dark' | 'system'
```

`useTheme()` is one shared state for the app: the persisted mode, the resolved
`isDark`, and `toggle()`. It writes the `dark` class on `<html>`, which is what
the stylesheet and the `dark:` variant key on, and stores the choice under
`vueuse-color-scheme` -- the key vueuse's `useDark` uses, so an app moving from
`useDark` keeps every user's saved preference, and a component still calling
`useDark()` stays in step through storage. Setting `isDark` to what the system
already shows returns the mode to `system` rather than pinning it.
`<ThemeToggle />` is the header's ghost icon button (sun/moon) over that state;
use it rather than re-creating the button per app.

## Session store

```ts
import { createSessionStore } from '@bboxsys/ui'

export const useSessionStore = createSessionStore({
  storageKey: 'myapp.session',              // required, unique per app
  serverName: 'myapp',                       // used in the "is the X server running?" error
  // adminBase: () => import.meta.env.VITE_ADMIN_URL || '',  // admin console only
})
```

`createSessionStore` returns a Pinia `defineStore` (id `session` unless
`id` is passed). Without `adminBase` the store fetches `/serviceendpoints/`
from its own origin and logs in at `<login_service>/api/v1/login`; with
`adminBase` it uses `<adminBase>/serviceendpoints/` and
`<adminBase>/api/v1/login` (an empty base means same-origin, as in the
embedded admin SPA).

State and getters: `session` (the persisted `StoredSession` or null),
`endpoints`, `user`, `accountId`, `isSuperAdmin`, `isTenantAdmin`,
`canAdminister`, `isAuthenticated`, `auth` (a `RequestAuth` for
`request()`).

Actions: `endpoint(kind)` (URL or ''), `loginBase()`, `bootstrapEndpoints()`
(once per page load), `login(email, password)`, `adopt(loginResponse)` (for
OAuth callbacks and invitation acceptance), `logout()`, `patchUser(patch)`,
`applyTheme()` (applies `user.theme_preference` through `useDark`).

The session persists under `storageKey` in localStorage and is restored on
load. Register `setUnauthorizedHandler` in `main.ts` to call `logout()` and
route to `/login` when a bearer request returns 401.

## LoginCard

```vue
<LoginCard
  :session="session"
  title="My App"
  description="Sign in to your fishing log"
  :logo="logoUrl"            // or <template #logo>...</template>
  :oauth-enabled="oauthEnabled"
  oauth-label="Continue with Google"
  @success="router.replace(redirect)"
  @oauth="startGoogleFlow"
/>
```

Props: `session` (the store instance, required), `title` (required),
`description`, `logo` (image URL), `logoAlt`, `oauthEnabled` (default
false), `oauthLabel`. Slots: `logo` (replaces the image), default (rendered
below the form, e.g. an app-level error line). Emits: `success` after
`session.login` resolves, `oauth` when the OAuth button is clicked.

The card calls `session.bootstrapEndpoints()` on mount, trims the email,
maps 401 to "Email or password is incorrect." and 403 to the locked-account
message, and shows `session.endpoint('login_service')` under the form.

## ServiceApiExplorer

```vue
<ServiceApiExplorer :session="session" primary="blackbox_service" />
```

Lists every HTTP(S)/WS(S) endpoint in `session.endpoints` except the kinds
in `exclude` (default `login_service`, `controller_service`), converts
ws(s) URLs to http(s), points each at `/api/v1/openapi.json`, sorts
`primary` first, prepends `include`, and renders `ApiExplorer` with the
session's bearer token and account id as auth headers. The admin SPA passes
`include` with its same-origin service and adds `admin_service` to `exclude`.

## Admin API types

`src/types/admin.ts` exports the wire types of the platform admin server
(`User`, `Account`, `Invitation`, `ServiceEndpoint`, `LoginResponse`,
`PKIStatus`, `LogEntry`, `StoreMetricsEntry`, NATS monitoring shapes, ...)
and the `ROLES` / `API_TOKEN_TYPES` / `API_TOKEN_SCOPES` constants. Import
them from the package root. They are transcribed from Go; keep them in step
with the server.

`APIToken` is the server's response shape (`APITokenView` in Go): it has
`scope`, `metadata`, and `has_token`, and no `token` field, because the
stored secret is never returned. Send `APITokenInput` on create and a
`Partial<APITokenInput>` on update; an absent or empty `token` keeps the
stored secret.

The AI assistant's wire types are here too: `AIStatus` (`enabled`,
`model` and `embedding` as `AICredentialRef | null`, `tools`,
`mcp_servers`, `allow_writes`), `AISession` / `AISessionSummary` /
`AISessionInput`, `AIMessage` with `AIToolCall` and `AIToolResult`,
`AIUsage`, `AIEvent` / `AIEventType` (the SSE frames of
`POST /api/v1/ai/sessions/{id}/messages`), `AIDocument`, and
`AISearchResult`. They mirror `ai/message.go`, `ai/session.go`,
`ai/service.go`, and `admin/ai.go` in Go.

## API token authentication

A stored API token authenticates either with the bearer itself
(`authMode: 'static'`) or with durable OIDC inputs the backend mints
short-lived bearers from (`client_credentials`, `password`,
`refresh_token`; `admin.APITokenResolver` in Go). `src/lib/apitokenAuth.ts`
is the browser side of that contract, and `ApiTokenAuthFields` is the form
section, so a product credential page adds only its own fields (name,
scope, endpoint, resource association) around them. The whole contract,
backend included, is in the platform's `docs/OIDC_API_TOKENS.md`.

```ts
// Read: the settings the server shows (no secrets).
readAPITokenAuth(token: APIToken): APITokenAuth
// Form state: every setting plus blank secrets.
emptyAPITokenAuthForm(overrides?: Partial<APITokenAuthForm>): APITokenAuthForm
apiTokenAuthForm(auth: APITokenAuth): APITokenAuthForm      // editing a stored token
// Validate before writing; previous is the stored auth when editing, else null.
validateAPITokenAuth(form: APITokenAuthForm, previous: APITokenAuth | null): string | null
// Write: sets id (the client ID), merges the oidc_* tags into metadata, and
// sets token only when a secret was entered (so a blank edit keeps the stored one).
withAPITokenAuth(body: Partial<APITokenInput>, auth: APITokenAuthInput): Partial<APITokenInput>
// Pieces, for callers that assemble the body themselves.
apiTokenAuthTags(auth): Record<string, string>
apiTokenAuthSecret(auth): string | undefined
hasOIDCSecret(auth): boolean
normalizeIssuer(url): string
API_TOKEN_AUTH_MODE_LABELS, OIDC_TOKEN_FIELD_LABELS, OIDC_CLIENT_AUTH_METHOD_LABELS
```

Wire shape (must match `admin/apitoken_oidc.go`): mode in the
`oidc_auth_mode` tag; issuer, scopes, username, token field, client auth
method, and the provider-only TLS opt-out in `oidc_*` tags; client ID in
`id`; client secret, password, and refresh token together as
`{"version":1,...}` in `token`. Entering any OIDC secret replaces the whole
set, which is why `validateAPITokenAuth` insists on the others the mode
needs.

```vue
<ApiTokenAuthFields
  v-model="form.auth"          // APITokenAuthForm
  :editing="!!editing"         // secrets optional, placeholders say "keep stored"
  audience="Example Cloud"     // "Token sent to Example Cloud"; default "the API"
  :modes="['static', 'client_credentials']"  // default: all four
  id-prefix="credential"       // element ids; default "apitoken-auth"
/>
```

The component renders the mode select, the bearer field for `static`, and
otherwise the issuer, client ID and secret, the password or refresh-token
fields for those grants (with a legacy notice on the password grant),
scopes, token field, token endpoint authentication, and the provider TLS
checkbox. It does not validate; call `validateAPITokenAuth` on submit.

## AI credentials

`src/lib/aiCredential.ts` is the browser half of the AI credential
contract; the Go half is `ai/credential.go` in the platform (the same
split as `apitokenAuth` and `admin/apitoken_oidc.go`). A model, embedding,
or MCP server credential is an ordinary API token whose
`metadata.resource_type` is `ai_model`, `ai_embedding`, or `mcp_server`,
whose `metadata.endpoint` is the base URL, and whose tags carry the
provider and model. The platform's `docs/AI.md` has the whole contract.

```ts
// Names, mirrored from Go
AI_RESOURCE_TYPE_MODEL | _EMBEDDING | _MCP_SERVER
AI_PROVIDER_TAG, AI_MODEL_TAG, AI_EFFORT_TAG, AI_MAX_OUTPUT_TAG, AI_CONTEXT_WINDOW_TAG,
AI_EMBEDDING_DIMENSIONS_TAG, MCP_TRUST_READ_ONLY_TAG, AI_TAGS
AI_PROVIDER_ANTHROPIC | _OPENAI_COMPAT | _VOYAGE | _OLLAMA, AI_PROVIDER_LABELS
AI_MODEL_PROVIDERS        // ['anthropic']
AI_EMBEDDING_PROVIDERS    // ['voyage', 'openai_compat', 'ollama']
AI_EFFORTS
AI_CREDENTIAL_USES, AI_CREDENTIAL_USE_LABELS   // 'general' plus the three resource types
// Form state and helpers
emptyAICredentialForm(overrides?): AICredentialForm   // {use, provider, model, effort, endpoint, trustReadOnly}
readAICredential(token: APIToken): AICredentialForm
withAICredential(body, form)       // sets resource_type and endpoint, writes the ai_* tags;
                                   // 'general' strips them and leaves other tags alone
validateAICredential(form): string | null   // rejects a model provider not in AI_MODEL_PROVIDERS
aiCredentialLabel(token): string | null   // "AI model: claude-opus-5-5", null for a general token
```

Presets for forms: `AI_PROVIDER_DEFAULT_ENDPOINTS` (the documented public
base URL per provider, matching the Go defaults, none for `openai_compat`),
`DEFAULT_CLAUDE_MODEL` and `CLAUDE_MODELS`, `claudeCredentialForm()` (a
form prefilled for a new Claude key), and `isClaudeCredential(token)`.

The admin Credentials page uses these on per-kind tabs; a product page
does the same and combines them with `withAPITokenAuth` when the
credential also authenticates with OIDC. Only `anthropic` has a model
provider in Go today, so `AI_MODEL_PROVIDERS` lists only it and
`validateAICredential` rejects any other provider for a model credential
("... is not supported for chat models yet"); `openai_compat` is an
embedding provider only. For an MCP server, `trustReadOnly` (a checkbox,
"Trust the server's read-only hints", on the admin Credentials page) writes
`MCP_TRUST_READ_ONLY_TAG` (`mcp_trust_read_only`) as `'true'`; without it
the assistant treats every tool of that server as a write. Offer it only
for a server the operator controls.

## HTTP client

`src/lib/http.ts`:

```ts
const outstanding: Ref<number>
class HttpError extends Error { status: number; body: string; get json(): unknown }
interface RequestAuth { token?: string; accountId?: string; basic?: { email: string; password: string } }
interface RequestOptions extends RequestInit {
  auth?: RequestAuth
  json?: unknown
  query?: Record<string, string | number | boolean | undefined>
}
function withQuery(url: string, query?: RequestOptions['query']): string
async function request<T>(url: string, opts?: RequestOptions): Promise<T>
```

- `auth.token` -> `Authorization: Bearer` plus `X-Blackbox-AccountID` when
  `accountId` is set (the platform auth contract); `auth.basic` ->
  `Authorization: Basic` (used only by login).
- `json` sets the body and Content-Type. `query` skips undefined/empty.
- Non-2xx throws `HttpError(status, bodyText)`; empty body resolves to
  `undefined as T`.
- Every call increments/decrements `outstanding`; the app shell renders THE
  ONE global progress bar from it -- components must not add their own
  HTTP loading bars.

## useAsync

`src/composables/useAsync.ts`:

```ts
function errorMessage(e: unknown): string
function useAsync<T>(fetcher: () => Promise<T>, initial: T): {
  data: Ref<T>; loading: Ref<boolean>; error: Ref<string>; run: () => Promise<T | undefined>
}
```

- `errorMessage` prefers an HttpError body's `.error`/`.message`, then
  friendly 401/403/404 text, then the body, then `Request failed (<status>)`.
  Note it is exported from useAsync.ts, not http.ts.
- `useAsync` uses shallowRef data and a generation counter (latest run
  wins); `run()` resolves `undefined` on failure instead of rejecting and
  stores the message in `error`.

## usePolling and usePagedList

```ts
function usePolling(fn: () => void, intervalMs = 10000, immediate = true): {
  active: Ref<boolean>; pause: () => void; resume: () => void
}
function usePagedList<T>(
  fetchPage: (limit: number, offset: number) => Promise<T[]>,
  fetchCount: () => Promise<number>,
  pageSize = 200,
): {
  rows: Ref<T[]>; total: Ref<number>; loading: Ref<boolean>; error: Ref<string>
  loaded: ComputedRef<number>; hasMore: ComputedRef<boolean>
  reload: () => Promise<void>; loadMore: () => Promise<void>
}
```

- `usePolling` calls `fn` on an interval, pauses while the tab is hidden,
  and calls it once on the way back. It does not call `fn` at start; a view
  loads in `onMounted` and polls after. The interval stops with the
  component. Use it instead of `setInterval` in a component.
- `usePagedList` is server-side "load more": `reload()` fetches the count and
  the first page, `loadMore()` appends the next. Errors land in `error` as
  `errorMessage` text.

## Formatting helpers

`src/lib/format.ts` -- display is imperial by default (matches the Go geo
package):

```ts
isZeroTime(iso?)            // true for empty or Go zero time 0001-01-01...
fmtInt(n)                   // en-US grouping
fmtFeet(meters)             // meters -> "x.x ft"
fmtMiles(miles)             // "x.xx mi"
fmtTempF(celsius)           // "x.x F"
fmtLatLon(v)                // 5 decimals
fmtPct(n)                   // "x.x%"
fmtDate(iso?)  fmtDateTime  // medium date + short time; '' for zero/invalid
fmtDay(iso?)                // medium date only
fmtRelative(iso?)           // "5 minutes ago" style; >30d falls back to fmtDay
fmtBytes(n)                 // B/KB/MB/GB/TB
shortKey(key, n = 8)        // "first8...last4" for long keys
shortUserId(id)             // "{accountID}:{email}" -> email
```

## Geo helpers

`src/lib/geo.ts`: `haversineMiles(lat1, lon1, lat2, lon2)` and
`pathMiles(points: { lat?: number; lon?: number }[])` (skips incomplete
points). `cn(...)` from `lib/utils` is the usual twMerge(clsx(...)).

## DataTable

Generic client-side table with search, 3-state sort, loading skeletons,
error alert, and empty states. This is the table itself; a list a reader acts
on goes through [ObjectTable](#objecttable), which puts the standard toolbar
over it. The search box is a `ClearableSearchInput`.

```ts
interface Column<T> {
  key: string
  label: string
  class?: string
  sortable?: boolean
  value?: (row: T) => string | number | undefined   // used for search, sort, and default cell text
}
```

Props:

| Prop | Type | Default |
|---|---|---|
| `columns` | `Column<T>[]` | required |
| `rows` | `T[]` | required |
| `rowKey` | `(row: T) => string` | required |
| `loading` | boolean | false |
| `error` | string | '' |
| `emptyText` | string | 'Nothing here yet.' |
| `searchable` | boolean | true |
| `searchPlaceholder` | string | 'Filter...' |
| `clickable` | boolean | false -- adds the pointer cursor and enables the `rowClick` emit |
| `rowClass` | `string \| (row: T) => string` | '' -- extra classes on each data row (e.g. `align-top` for multi-line cells) |
| `expandable` | `(row: T) => boolean` | every row, when the `expanded` slot is given |
| `expandTitle` | `(row: T, expanded: boolean) => string` | 'Expand row' / 'Collapse row' |
| `stickyHeader` | boolean | true -- the header row stays in view while the rows scroll inside the table |
| `maxHeight` | CSS length or `'available'` | `'available'` -- the table's height limit when `stickyHeader` is on: the space left below it in the viewport or the nearest bounded scroll parent; any CSS length (`18rem`, `calc(100dvh - 15rem)`) is used as is. A shorter table is unaffected |
| `batchSize` | number | 100 -- infinite scroll: rows are drawn a batch at a time as the reader nears the end; 0 draws all |
| `hasMore` | boolean | false -- the view loads pages from the server and has more; reaching the end emits `loadMore` |

Emits:
- `rowClick: [row: T]` (fires only when `clickable` is true), from a click
  on the row or from Enter or Space while the row has keyboard focus.
- `expand: [row: T, expanded: boolean]` when a caret is toggled; use it to
  load the detail lazily.
- `loadMore: []` when the reader reaches the end of the loaded rows while
  `hasMore` is true. It fires once per change of `rows` and never while
  `loading`; the view appends the next page to `rows`.

Slots:
- `#toolbar` -- extra controls beside the search input.
- `#cell-<key>` -- scoped `{ row, value }` per-column override
  (e.g. `#cell-name`, `#cell-modTime`).
- `#head-<key>` -- scoped `{ column }`; replaces a header cell's content
  (the label and sort arrows). The cell keeps its sort click.
- `#actions` -- scoped `{ row }`; adds a trailing column whose cell has
  `@click.stop`, so action buttons do not trigger `rowClick`.
- `#expanded` -- scoped `{ row }`; giving it adds a leading caret column, and
  a toggled row renders the slot in a full-width row directly beneath it.
  `expandable` limits the caret to rows that have something to show (a row
  it refuses keeps the empty cell, so the columns stay aligned). Expansion
  state is keyed by `rowKey`, survives a refresh that replaces the row
  objects, and is dropped for rows that disappear. The caret cell stops
  propagation, so it never triggers `rowClick`.

The header is fixed by default. A header cell cannot stay in view while the
whole page scrolls past a table, so with `stickyHeader` the rows scroll
inside one container (both axes) limited to `maxHeight`, and the header
cells stick to its top.

**Available height.** The default `maxHeight`, `'available'`, measures the
space between the table's top and the bottom of the viewport (the visual
viewport when there is one), lowered to the bottom of any ancestor that
scrolls or is itself height-bounded (`overflow-y` auto, scroll, hidden or
clip with overflowing content or a `max-height`), less that ancestor's
bottom padding. The result, minus a 16 px gap, is the container's
`max-height`, never below 160 px so a table that starts near the bottom of a
short dialog stays usable. It is a maximum: a short table shrinks to its
rows. The measurement runs when the table mounts, when the window or visual
viewport resizes, and when the table or any ancestor changes size (one
ResizeObserver, measured on the next animation frame so observers cannot
feed back on themselves); ancestor scroll offsets are added back so
scrolling the page does not change the height. The app still owns the
layout that establishes the space: its toolbar, notices, the flex chain
(`min-h-0 flex-1 overflow-auto` on the content root) and panel boundaries.
Two tables sharing one panel, or a table in a form section, pass an explicit
`max-height` (`18rem`) because the measurement cannot know how the space
should be divided; `:sticky-header="false"` lets a table grow with the page.

**Infinite scroll.** Every table scrolls infinitely. Client-side lists are
drawn `batchSize` rows at a time: a sentinel after the last drawn row is
watched with an IntersectionObserver on the scroll container, and nearing
it draws the next batch (a new filter or sort starts over from the first
batch; `batchSize` 0 draws every row at once). Batching bounds the initial
render, not the DOM: scrolling to the end eventually draws every supplied
row. It is not virtualization, and a bounded DOM window for very large live
lists would be a separate feature.

**Server-paged lists.** The component renders the rows it is given and asks
for more; the view owns fetching, cursors, deduplication and what happens
when the query changes. Set `hasMore` while the server has rows beyond
`rows`; once every supplied row is drawn and the sentinel comes into view
the table emits `loadMore`, and the view appends the next page to `rows`.
The request is latched: `loadMore` is not emitted while `loading` is true,
and after one emit not again until the `rows` reference changes (append by
replacing the array, not by pushing into it, or the next page is never
asked for; a failed fetch re-arms by assigning `rows` again). Keep rows
keyed by an id and drop duplicates on append, because an offset shifts when
new rows arrive at the top (the admin Logs page does this, and its Follow
switch prepends new entries without dropping loaded ones). Local filtering,
sorting and select-all cover the supplied rows, drawn or not, and cannot
cover records the server has not sent; global search, ordering, totals or a
true select-all need their own server contract. Do not add Previous / Next
pagers to new tables.

**Keyboard.** A `clickable` row is in the tab order (`tabindex="0"`, with a
focus ring) and Enter or Space on the row itself activates it. Keys pressed
in a button, link or checkbox inside the row reach that control only, and
Space scrolls the page as usual anywhere a row is not activated.

```vue
<DataTable :columns="columns" :rows="policies" :row-key="r => r.name"
           :expandable="r => r.rules.length > 1" clickable @row-click="open">
  <template #expanded="{ row }"><RuleList :rules="row.rules" /></template>
</DataTable>
```

## ObjectTable

The standard table of every app: a `DataTable` under one toolbar. Use it for
any list of things a reader selects, inspects, edits or deletes; use a bare
`DataTable` only for a read-only listing that needs no toolbar of its own.

The toolbar has a fixed order, so a table reads the same in every view of
every app:

| Left | Right |
|---|---|
| the filter, then `#filters` and `#toolbar` | the count, inspect / edit / delete for the selection, `#selection-actions`, then `#toolbar-actions` (refresh, create) |

Select-all is not in the toolbar: it heads the checkbox column in the
table's header row, lined up with the row checkboxes, and stays in view with
the sticky header. It selects every row that passes the filter, across
pages; its state is indeterminate when some are selected.

Rows are acted on through the SELECTION rather than a button per row, so one
row and forty are deleted the same way. A delete is confirmed by typing what
is being deleted: the row's label for one row, `DELETE <n>` for several.
Deletion runs a row at a time and continues past a failure; the rows that
failed stay in the dialog with their messages and can be retried, and the
rows that succeeded are not repeated.

It takes every `DataTable` prop, plus:

| Prop | Type | Default |
|---|---|---|
| `selectable` | boolean | true -- the checkbox column, with select-all in its header |
| `stickyHeader`, `maxHeight` | | as `DataTable`: on, `'available'` |
| `batchSize`, `hasMore` | | as `DataTable`; `loadMore` is forwarded. The filter, the count and the selection cover every supplied row, not only the drawn batch |
| `pageSize` | number | deprecated since 0.18.0 -- read as a fallback batch size; there is no pager |
| `inspect` | boolean | true -- whether `clickable` also adds the inspect action; off for a table whose row click does something other than open the row |
| `selectionScope` | string | none -- when it changes the selection is dropped and a deletion in flight stops before its next row |
| `rowLabel` | `(row: T) => string` | the row's `name`, then `key`, then `rowKey` |
| `rowDescription` | `(row: T) => string` | the row key, when it differs from the label |
| `rowSearchText` | `(row: T) => string` | none -- text to match beyond the columns |
| `fuzzySearch` | boolean | false -- also match a term's characters in order, not adjacent |
| `edit` | `(row: T) => unknown` | none -- giving it adds the edit action |
| `remove` | `(row: T) => Promise<unknown>` | none -- giving it adds the delete action and the confirmation |
| `canEdit` / `canRemove` | `(row: T) => boolean` | every row -- one refused row disables the action for the selection |
| `deleteDescription` | string | 'This action cannot be undone.' |
| `noun` / `nounPlural` | string | 'object' / `noun + 's'` -- what a row is called in labels and the confirmation |
| `accent` | `AccentTone` | none -- frames the table as an accent surface |

The filter matches every whitespace-separated term somewhere in the row, in
any column, so `prod web` finds the row whose zone is prod and whose name is
web. `clickable` adds the inspect action for the one selected row as well as
the row click; both emit `rowClick`.

Emits: `rowClick`, `expand`, `loadMore` (as `DataTable`),
`selectionChange: [rows: T[]]`, and `removed: [rows: T[]]` after a deletion
that removed at least one row.

**Breaking change in 0.18.0.** The Previous / Next pager and the
`displayRows` prop are gone: pass the whole row set as `rows` and let the
table draw it in batches (`batchSize`), or set `hasMore` and append pages on
`loadMore` for a server-paged list. `pageSize` is read as a batch size for
callers that still pass it. The default height of both tables changed from
`70vh` to the measured available space, which changes the layout of pages
that relied on the fixed fraction; pass an explicit `max-height` where the
old value was right.

Slots: `#cell-<key>`, `#actions` and `#expanded` are forwarded to the
`DataTable`. `#filters` and `#toolbar` sit beside the filter;
`#toolbar-actions` (scoped `{ selected, busy }`) and `#selection-actions`
(scoped `{ row }`, rendered while exactly one row is selected) sit on the
right.

The component knows nothing about routing, sessions or toasts, and that is
deliberate. An app that wants the selection cleared on navigation or on an
account change folds those into `selectionScope`, and reports a deletion from
`removed`:

```vue
<ObjectTable
  :columns="columns" :rows="routes" :row-key="r => r.id"
  noun="route" clickable
  :selection-scope="`${route.fullPath}|${session.accountId}`"
  :remove="r => api.remove(r.id)"
  @removed="rows => { toast.success(`Deleted ${rows.length}`); reload() }"
  @row-click="open"
>
  <template #toolbar-actions>
    <ObjectAction action="refresh" :loading="loading" @click="reload" />
    <ObjectAction action="create" label="New route" @click="create" />
  </template>
</ObjectTable>
```

An app with its own table vocabulary (accents named by color, a toast on
every deletion) wraps `ObjectTable` once and forwards the slots,
rather than restating the scope and the toast in every view.

`lib/objectTable` holds the rules behind it -- `objectLabel`,
`selectedVisibleRows`, `retainedSelection`, `removeObjects` -- for a view
that runs the same selection or deletion without the table.

## Page scroll focus (focusPageScroll)

After a reader clicks inside a table (a checkbox, a sortable header), the
keyboard belongs to the table's scroll container, and the arrow keys and
Space no longer scroll the page around it. `focusPageScroll` is a click
handler that gives the keyboard back to the page: after an unmodified
left click on background content it focuses the nearest overflowing scroll
region above the click (falling back to the nearest scrollable ancestor,
then to the element it is attached to) with `preventScroll`, adding a
temporary `tabindex="-1"` that is removed again on blur unless the element
had one of its own.

It is opt-in and never installed by importing the kit or mounting a table.
An app attaches it once to its content root:

```vue
<script setup lang="ts">
import { focusPageScroll } from '@bboxsys/ui'
</script>
<main class="min-h-0 flex-1 overflow-auto" @click="focusPageScroll">
  <RouterView />
</main>
```

It does nothing when the click was already handled (`defaultPrevented`),
carries a modifier key, lands on a control or inside one (links, buttons,
form fields, labels, summaries, media, canvas and SVG, contenteditable, the
Monaco and CodeMirror editors, elements with an interactive ARIA role, and
anything with a non-negative `tabindex` other than a tab panel, region or
document), lands inside a table (the kit's tables carry
`data-slot="table-container"`), lands in a dialog, while a modal is open, or
while text is selected. A custom widget that manages its own focus opts out
with `data-scroll-focus="preserve"` on its root.

## ObjectAction / ClearableSearchInput

The toolbar's parts, for a view that lays out a toolbar without a table.

- `ObjectAction`: props `{ action, label?, disabled?, loading? }`, emits
  `click`. `action` is `'create' | 'inspect' | 'edit' | 'delete' |
  'refresh'` and fixes the icon and the treatment: create is the one filled
  button, delete the one red mark, refresh spins while `loading`. `label` is
  the tooltip, the title and the accessible name. A page-level refresh or
  create button is an `ObjectAction` too, not a text button.
- `ClearableSearchInput`: `v-model` string, props `{ placeholder?,
  ariaLabel?, clearLabel?, disabled?, class?, inputClass? }`. The clear
  button appears once there is text and returns focus to the field. Other
  attributes fall through to the input. `DataTable` and `ObjectTable` use it
  for their own filter.

## ConfirmDialog

```vue
<ConfirmDialog
  v-model:open="confirmOpen"
  title="Delete thing?"
  description="This cannot be undone."
  confirm-text="Delete"
  destructive
  :action="doDelete"       // () => Promise<unknown>
  @done="..."               // after success (dialog closes itself)
  @error="m => toast.error(m)"  // errorMessage(e); dialog stays open
/>
```

While the action runs the confirm button shows "Working" and both buttons
disable.

## PageHeader / StatusBadge

- `PageHeader`: props `{ title, description? }`, default slot renders
  right-aligned actions.
- `StatusBadge`: prop `{ status: string }`; tones: emerald for
  valid/accepted/active/healthy, amber for pending/expiring/warn, red for
  expired/revoked/locked/error/fatal, neutral otherwise.

## PageTabs

The standard tab bar for every app: flat, left-aligned tabs on a bottom
border, the active tab underlined in the brand color (`border-primary`,
which every brand preset maps to `--brand`). Use it for any row of tabs
that switches a page's content; do not hand-roll a tab bar or use the
shadcn pill-style `Tabs` for page sections.

- Props `{ tabs: PageTab[], modelValue?: string, label?: string }`.
  `PageTab` is `{ label, value?, to?, count?, disabled? }`.
- **Value tabs** (`value` on each tab, `v-model` on the bar) switch content
  inside one page. They render as a `role="tablist"` of buttons with
  `aria-selected`, and the arrow keys move between enabled tabs. Keep the
  selection in the route query when it should survive a reload (the admin
  Credentials page uses `?tab=`).
- **Route tabs** (`to` on each tab) link to child routes. The active tab is
  the link the router marks `aria-current="page"`. The kit does not depend
  on vue-router: it uses the app's registered `RouterLink`, or a plain
  anchor in an app without one. A bar does not mix the two kinds.
- `count` shows a small number after the label when it is positive.
- The `actions` slot sits at the right end of the bar.
- Spacing comes from the caller: pass `class="px-6"` for a full-width bar
  under a `PageHeader`, or put the bar inside the page's padded container.

```vue
<PageTabs v-model="tab" :tabs="[{ value: 'tokens', label: 'API tokens' }, { value: 'claude', label: 'Claude', count: 1 }]" />
<PageTabs :tabs="[{ to: '/accounts/a/users', label: 'Users' }, { to: '/accounts/a/agents', label: 'Agents' }]" class="px-6" />
```

## AccentSurface / MetricTile / TermList

- `AccentSurface`: props `{ tone?: AccentTone, as?: string, interactive?:
  boolean }`; a framed surface with a top rail and a wash at one accent.
  `AccentTone` is `'brand' | 'highlight' | 'chart-3' | 'chart-5'` -- named by
  the token, not by a color, because the same token is blue under one brand
  and orange under another. `as` sets the element (default `section`),
  `interactive` adds the hover lift for a surface that is a link or a button.
  The classes are `ACCENT_SURFACE` in `theme/tone.ts`.
- `MetricTile`: props `{ label, value?, caption?, icon?, rail?, meter?, dot?,
  to?, clickable? }`, emits `click`. A label, a headline number, and either a
  caption or a 0-100 `meter` colored by the rail (`RAIL_METER`). `rail` is a
  `Rail` (default `brand`); `dot` puts a status dot beside the value for a
  reading that is a state. `to` makes the tile a link -- the app's registered
  `RouterLink` when it has one, a plain anchor otherwise, so the kit needs no
  router -- and `clickable` makes it a button that emits `click`.
- `TermList`: props `{ terms: string[], limit?: number, empty?: string,
  mono?: boolean }`; a capped row of badges for a table cell, with a `+n`
  badge whose title carries the rest. Scoped slots `#term="{ term }"` and
  `#overflow="{ terms }"` replace a term badge or the overflow badge (a
  link per term, a tone per state); without them the default badges render.

## AppNotice / AppNoticeBand

A page-level notice for an OPERATIONAL condition: something is wrong with the
data on screen or the reach behind it. Explanation belongs in help, not here.

An app mounts `<AppNoticeBand />` once, under its top toolbar and above the
page. A view raises `<AppNotice tone="warn">...</AppNotice>` anywhere in its
template and it teleports into the band, so notices always appear in one
place and leave with the view that raised them.

- `AppNoticeBand`: prop `{ id?: string }`, default `app-notices`.
- `AppNotice`: props `{ tone?: Tone, icon?: Component, to?: string, inline?:
  boolean }`. `tone` defaults to `warn` and picks the mark and the
  `TONE_NOTICE` classes; `to` is the band's selector; `inline` renders the
  notice where it is written, for a surface with no band above it.

## GeoMap

Leaflet map with canvas-rendered markers, built for large datasets
(10k+ waypoints).

```ts
interface MapMarker  { id: string; lat: number; lon: number; color?: string; label?: string }
interface MapLine    { id: string; points: [number, number][]; color?: string; label?: string }
interface MapPolygon { id: string; points: [number, number][]; color?: string; label?: string }
```

Props: `markers`, `lines`, `polygons` (default `[]`),
`selectedId?: string | null`, `fitKey?: string | number` (change it to
re-fit bounds), `tile?: 'osm' | 'satellite' | 'topo'` (default osm).

Emits: `select: [id, kind: 'marker' | 'line' | 'polygon']`,
`mapClick: [lat, lon]`. Expose: `fit()`.

Implementation constraints (do not regress):
- Leaflet loads lazily in onMounted, so apps without maps do not bundle it.
- `preferCanvas: true` + `L.circleMarker` -- one painted layer instead of a
  DOM node per marker. divIcon markers melted the main thread at 10k
  points.
- Root div carries `isolate z-0` so Leaflet's internal z-indexes (up to
  1000) stay below app popovers/selects/dialogs at z-50.
- Selected marker: radius 9 + white ring and the map pans to it. Defaults:
  markers #3b82f6, lines #f97316, polygons #0ea5e9; dark mode dims tiles
  except satellite.

## ApiExplorer

Self-contained OpenAPI 3 explorer page with try-it.

```ts
interface ApiService {
  name: string     // display name, e.g. "blackbox_service"
  baseUrl: string  // '' for same-origin
  specUrl: string  // the service's openapi.json URL
}
```

Props: `services: ApiService[]`, `authHeaders?: () => Record<string, string>`.
Pass `authHeaders` as a FUNCTION so each request re-reads the current
token. The wrapper must give it bounded height (`h-full min-h-0`) -- it
scrolls internally. A typical mount
derives `services` from the session endpoint map and returns
`Authorization: Bearer` + `X-Blackbox-AccountID` from `authHeaders`;
a simpler app mounts it with a single same-origin service and no auth.

Every bboxsys service serves its spec at `GET /api/v1/openapi.json`
(unauthenticated).

The result badge and the documented responses show the standard reason
phrase beside each status code (`200 OK`, `412 Precondition Failed`), from
`httpStatusText(code)`, which the kit exports for an app that renders status
codes of its own; it returns `''` for a code it does not know. A documented
description that merely repeats the phrase is not shown twice.

## Assistant

The AI assistant conversation (platform `docs/AI.md`, sections 12, 13 and
17), for the admin console's Assistant page and for any SPA that wants
the assistant beside its own views. The backend is the admin server's:
the admin's own assistant at `/api/v1/ai`, every other (a product's
service assistants and `all`, the coordinator) relayed at
`/api/v1/ai/services/{id}`. Everything acts as the signed-in user; the
browser holds no model key.

```ts
import { createAssistantClient, ADMIN_ASSISTANT, ALL_ASSISTANT } from '@bboxsys/ui'

const client = createAssistantClient({
  base: () => session.endpoint('admin_service'),  // '' in the admin console
  auth: () => session.auth,
})
const services = await client.services()   // [{id, name}]: admin, the services, all
const api = client.for('kubernetes')       // status, sessions.{list,create,get,rename,remove}, sendMessage
```

`services()` answers the admin alone when the server has no gateway (404).
`sendMessage(id, content, onEvent, signal)` streams the run's events
(`AIEvent`) with the kit's own SSE parser (`sseParser`, exported), since
`request` waits for the whole body; aborting the signal ends the run.

`AssistantChat` is the conversation with ONE assistant: the sessions, the
transcript of the open one (tool calls and results collapsed inline),
the streamed run, token usage and the composer.

Props: `api: AssistantApi` (bound to one assistant; key the component by
the assistant id so a switch remounts it and no reply can land in another
store), `layout: 'page' | 'panel'` (`page` lists sessions in a sidebar,
`panel` keeps them in a dropdown above the transcript), `serviceLabel`
(names the assistant in the composer). `v-model:busy` is true while
anything is in flight, for the host to disable its assistant picker.
Emits `error(message)` for the host's toasts. Slot `#unconfigured`
replaces the notice shown when the caller has no model credential (the
console links its Credentials page there). The host gives it bounded
height (`h-full min-h-0`); the transcript scrolls inside.

`AssistantDrawer` is the assistant as a right-side drawer over the
current page: a header with a picker for which assistant answers and
`AssistantChat` in its panel layout, keyed by the choice.

Props: `client: AssistantClient`, `services: AssistantService[]` (what
`client.services()` returned), `v-model:open`, `v-model:service` (the id
in use; the host sets the default, normally the assistant of the module
on screen, and may follow the module as the route changes), `title`,
`description`. Emits `error`; forwards `#unconfigured`. The drawer holds
no app knowledge: which assistants exist, which module is on screen and
where credentials live all come from the host. An app shows the toolbar
button that opens it only when the current module has an assistant (the
gateway ids are the registered service ids).

`AssistantMarkdown` renders a reply: Markdown through `marked`, sanitized
by DOMPurify (links open in a new tab without the opener), with every
fenced block tagged `echarts` drawn by `AssistantChart` under the kit's
chart theme (JSON only; no code runs). Props: `text`, `streaming` (an
unfinished chart block shows a placeholder). The renderer and ECharts are
loaded on first use.

## Documentation site and gate

The browser and Starlight half of the platform's embedded documentation
(`docs/DOCSITE.md` in the platform repository has the whole contract; the
Go half is the `docsite` and `ai/docsearch` packages).

`@bboxsys/ui/starlight` (`src/starlight/config.mjs`, plain ESM like
`./vite` because Astro's config is loaded by Node) exports `docsSite`,
`rehypeBaseLinks`, `withBase`, `DEFAULT_DOCS_BASE` and `THEME_CSS`. A
product's `astro.config.mjs`:

```js
import { defineConfig } from 'astro/config'
import starlight from '@astrojs/starlight'
import { docsSite } from '@bboxsys/ui/starlight'

export default defineConfig(docsSite({
  integration: starlight,          // the product's Starlight, never resolved by the kit
  title: 'Acme',
  base: '/docs',                   // the path the Go handler mounts the site at
  description, logo, favicon, social, sidebar,
  brand: '../acmevue/src/brand.css',     // the SPA's brand, mapped onto the site's themes
  customCss: ['./src/styles/custom.css'],   // after the theme and the brand
  starlight: { ... },              // further Starlight options
  markdown, integrations, build, vite, ...  // further Astro options
}))
```

It sets `base`, `trailingSlash: 'ignore'`, directory output, Pagefind,
the kit theme first in `customCss`, and prepends `rehypeBaseLinks` to the
rehype plugins so root-relative `href`/`src` in Markdown and in MDX
component attributes (`LinkCard href`) are prefixed with the base; links
in frontmatter (hero actions) are not content and carry the base
themselves. `@bboxsys/ui/starlight.css` is the theme: surfaces tinted
toward `--brand-hue`, Geist, the kit's chart tokens for builders shared
with the SPA, and the themed ECharts tooltip. `brand` names the SPA's
`src/brand.css` (relative to the Astro root); `docsSite` serves it as the
virtual stylesheet `BRAND_CSS_ID` through `brandCSSPlugin`, with the
file's `:root` tokens as the light theme and `.dark` layered over them as
the dark one, so the hue and the chart slots follow the app with no copy
in the docs tree. A product with no brand file sets `--brand-hue` in its
own `customCss` instead. `@astrojs/starlight` is an optional peer
dependency.

`lib/docsSession.ts`: `openDocsSession(auth, base?)` posts the user's
bearer to `{base}/_session` with `credentials: 'include'` and the server
sets the session cookie; `closeDocsSession(base?)` deletes it quietly
(call it at sign-out); `docsTarget(next, base?)` is the page to continue
to after the gate, accepting only a path under the base so the gate is
never an open redirect; `docsSessionUrl(base?)`.

`DocsGate` is the page the documentation server redirects to when a
browser has no session. Props: `auth: RequestAuth` (normally
`session.auth`), `base` (default `/docs`), `next` (the server's query
parameter, read by the app's router and passed in; the kit does not
depend on vue-router), `title`, `navigate` (replaces
`window.location.replace`, for tests). On mount it opens the session and
continues to `docsTarget(next)`; a refusal shows why (403: not allowed;
401: sign in again; otherwise unavailable) with a retry, and emits
`error`. The app mounts it on a route behind its sign-in guard (an
anonymous visitor signs in first and lands back with `next`), links to
`{base}/` from its shell, and proxies `/docs` in its Vite dev server
beside `/serviceendpoints`.

## Vendored shadcn primitives

`src/ui/*` holds the shadcn-vue primitives (button, dialog, select, table,
badge, input, checkbox, tooltip, progress, ...) used BY the kit's own
components; they are not exported
from the barrel. Apps keep their own copies under `@/components/ui/`.
Inside this repo, components import them relatively (`../ui/button`;
api-explorer files use `../../ui/button`).
