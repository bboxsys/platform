# @bboxsys/ui Developer Guide

Shared Vue 3 UI kit for apps built on the platform, consumed as an
**npm package that ships raw source** -- the consuming Vite app compiles it.
Full API reference: `docs/DEVELOPER.md` (components, props/slots/emits,
lib functions, consumer contract). Read it before changing exports.

## Rules

- No emojis in code, comments, or docs. No AI-tool attribution in commits.
- This package lives in `bboxsys/platform` and is released WITH the
  platform: tagging `v0.x.y` on platform main runs `.github/workflows/
  release.yml`, which publishes `@bboxsys/ui` at that version to GitHub
  Packages. `version` in package.json is informational; the tag wins.
- Everything consumers use is exported from `src/index.ts` (the barrel).
  Apps import only from the package root -- never add code that requires
  deep imports. The one exception is `@bboxsys/ui/vite` (`src/vite/`), the
  dev-server helpers an app's `vite.config.ts` imports: plain `.mjs` with a
  `.d.mts`, because Node loads that config rather than the bundler. Browser
  code must never import it. `brandTokens()` there is the one build-time
  plugin: it copies the `:root` / `.dark` token blocks of the app's
  `src/brand.css` to `dist/brand-tokens.css`, which the Go host hands to
  the admin console through `admin.HostBranding`; `docsSite({ brand })`
  maps the same file onto the docs site. Keep `extractBrandTokens` the one
  reader of a brand file.
- All runtime deps are `peerDependencies` (leaflet optional). Never add a
  `dependencies` section; the consuming app owns the versions.
- `src/types/admin.ts` mirrors the Go wire types of the platform admin
  server. When a Go response shape changes, change the type here in the
  same release.
- `src/lib/apitokenAuth.ts` and `ApiTokenAuthFields` are the browser half
  of the API token authentication contract (static bearer vs. OIDC modes;
  `oidc_*` tags and the JSON secret set) whose Go half is
  `admin/apitoken_oidc.go`. Product credential pages compose them and add
  only their own fields; do not fork the OIDC form per app.
- `src/lib/aiCredential.ts` is the browser half of the AI credential
  contract (resource types `ai_model`, `ai_embedding`, `mcp_server`; the
  `ai_*` tags; provider ids and labels; `readAICredential`,
  `withAICredential`, `validateAICredential`, `aiCredentialLabel`) whose Go
  half is `ai/credential.go`, the same pattern as `apitokenAuth`. The AI
  wire types (`AIStatus`, `AISession`, `AIMessage`, `AIEvent`, ...) are in
  `src/types/admin.ts` and mirror `ai/message.go`, `ai/session.go`, and
  `ai/service.go`. Change them with the Go side in the same release.
- A SPA's service URLs come from the server's `/serviceendpoints/` map, so
  moving a dev proxy does not change where it signs in. `devEndpoints()`
  (BBOX_DEV_ENDPOINTS) rewrites that map for the dev server only -- keep
  dev-only overrides there, never in runtime code.
- `createSessionStore` is the one session store. Apps wrap it with their
  own `storageKey`; only the admin console passes `adminBase` (same-origin
  API), every other SPA discovers `login_service` from `/serviceendpoints/`.
- `LoginCard` owns the form, error mapping, and login-service footer. Apps
  supply title, branding (`logo` prop or `#logo` slot), and route on
  `success`. Do not fork it per app.
- Leaflet is loaded lazily (`await import('leaflet')` in GeoMap's
  onMounted) so non-map apps do not ship it. Keep it that way. Type-only
  imports (`import type * as LT from 'leaflet'`) are fine.
- GeoMap renders markers as canvas `circleMarker`s with `preferCanvas`
  (10k+ points must stay cheap) and keeps `isolate z-0` on its root so
  Leaflet's z-1000 panes stay below app popovers. Do not reintroduce
  divIcon markers or remove the isolation.
- shadcn-vue primitives live under `src/ui/` for the kit's own components
  and are NOT in the barrel. Components inside `src/components/` import
  them as `../ui/<name>` (api-explorer files: `../../ui/<name>`).
- Styling: Tailwind v4 utility classes against the shared CSS variables
  (`--background`, `--primary`, `--border`, ...). The consumer's CSS must
  `@source` this package's `src` -- if a class you use here does not show
  up in an app, that line is the first suspect.
- `src/theme/theme.css` (published as `@bboxsys/ui/theme.css`, the second
  non-TypeScript entry point beside `./vite`) is the ONE place a shared
  color is defined: brand, status and chart tiers, the chrome, the `dark`
  variant, both themes, the `@theme inline` utilities over them. Apps
  import it and then a brand preset from `src/theme/brands/` (`blue`, the
  default; `orange`) or their own five-token brand block;
  they do not copy the token block. A brand that belongs to one product
  (a product's own palette) stays in that product, written
  once in its `src/brand.css` so `brandTokens()` and `docsSite({ brand })`
  can carry it to the admin console and the docs. Every token stays in oklch,
  hex or rgb() because `src/theme/tokens.ts` (`token`, `palette`,
  `themeEpoch`) parses those forms to hand canvas renderers a hex. Status
  FILLS are the same in both themes on purpose (a verdict stays
  green/amber/red under any brand); only the `-text` variants are themed.
  The theme mode is `useTheme` (shared state over vueuse's `useColorMode`,
  under the storage key `useDark` used so saved preferences carry over)
  and `ThemeToggle` is the header button; do not add a per-app toggle.
  The chart helpers (`chartTheme`, `STATUS`, `utilizationBand`,
  `baseTimeChartOption`) and the tone maps (`TONE_*`, `RAIL`) read the
  tokens and hold no literal color; keep it that way.
- `ObjectTable` is the standard table and its toolbar order is the
  standard: filter and the view's filters on the left; count,
  selection actions and the view's own actions on the right. Toolbar buttons
  are `ObjectAction`s and every search box is a `ClearableSearchInput`; do
  not hand-roll either in an app. `ObjectTable` must stay free of
  vue-router, the session store and toasts -- none is a peer dependency --
  so an app passes what ends a selection as `selectionScope` and reports a
  deletion from `removed`. `MetricTile` reaches `RouterLink` through the
  app's registered components for the same reason.
  Select-all heads the checkbox column in the header row, not the toolbar,
  and the header is sticky by default (rows scroll inside the table up to
  `maxHeight`, whose default `available` measures the space left below the
  table in the viewport or the nearest bounded scroll parent, at least
  160 px; an embedded table passes a CSS length); keep both. Tables scroll
  infinitely: `DataTable` draws `batchSize` rows at a time and, for a
  server-paged list, emits `loadMore` while `hasMore`; `ObjectTable`
  forwards all three and passes the whole filtered set, so counts and
  select-all cover rows not yet drawn. There is no pager and no
  `displayRows` since 0.18.0 (`pageSize` is a deprecated batch size). A
  clickable row is focusable and Enter / Space activates it.
  `focusPageScroll` (`lib/pageScrollFocus.ts`) returns keyboard scrolling
  to the page after a background click; it is opt-in, attached by the app
  to its content root, never installed by the kit.
- `PageTabs` is the standard tab bar (flat tabs on a bottom border, the
  active one underlined in the brand color), for value tabs (`v-model`)
  and route tabs (`to`). Apps use it for page sections instead of a
  hand-rolled bar or the pill-style shadcn `Tabs`. Like `MetricTile`, it
  reaches `RouterLink` through the app's registered components and must
  stay free of a vue-router import.
- The assistant conversation (`lib/assistantApi.ts`: `createAssistantClient`,
  the SSE parser; `components/assistant/`: `AssistantChat`, `AssistantDrawer`,
  `AssistantMarkdown`, `AssistantChart`) is the one implementation of the
  platform's conversation API in the browser: the admin console's Assistant
  page and a product's toolbar drawer both mount it. It knows nothing of
  routes or toasts (slot `#unconfigured`, `error` emit, `v-model:busy`).
  `marked`, `dompurify` and `echarts` are peers, resolved at type-check time
  and loaded on first use, so an app pays for them only when a reply is
  rendered; keep those imports dynamic.
- `src/starlight/` (`@bboxsys/ui/starlight`, `@bboxsys/ui/starlight.css`)
  is the third non-TypeScript entry point beside `./vite` and `./theme.css`:
  `docsSite()` builds a product's Astro configuration around the Starlight
  integration the product passes (never resolved here: a linked kit has no
  node_modules), with the mount base, the base-link rehype plugin and the
  kit's Starlight theme; `@astrojs/starlight` is an optional peer. Its
  browser half is `lib/docsSession.ts` (`openDocsSession`,
  `closeDocsSession`, `docsTarget`) and `DocsGate`, the page an app mounts
  on the route the Go `docsite` handler redirects to. The Go half and the
  whole contract are the platform's `docs/DOCSITE.md`.
- Accents are named by token (`AccentTone`: `brand`, `highlight`, `chart-3`,
  `chart-5`), never by color: the kit is themed per app, so a tone called
  `blue` would be orange under another brand. A product that names its accents by
  color maps its names onto these in the product.
- Verify: `npm run typecheck && npm test` here, then build the admin SPA
  (`cd ../admin/ui && npm run build`), which consumes this directory by
  path and type-checks it with `noUnusedLocals`-style strictness.

## Consumers

The admin SPA (`../admin/ui`, by path) and every product SPA built on the
platform, each from the published package.
Breaking a component breaks all of them on their next bump -- keep changes
backward compatible within a minor line or update consumers deliberately.
