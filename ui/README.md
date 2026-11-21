# @bboxsys/ui

[![npm](https://img.shields.io/npm/v/@bboxsys/ui)](https://www.npmjs.com/package/@bboxsys/ui)
[![license](https://img.shields.io/npm/l/@bboxsys/ui)](./LICENSE)

The shared Vue 3 UI kit for apps built on the
[bboxsys platform](https://github.com/bboxsys/platform): one theme, the
sign-in flow, and the components every console page is made of.

It ships as raw `.vue` and `.ts` source. Your Vite app compiles it with its
own Vue and Tailwind v4 toolchain, so there is no build step and no bundled
copy of your dependencies.

## What's inside

| Area | What you get |
|---|---|
| **Theme** | One token stylesheet with light and dark, brand presets (`blue`, `orange`), `ThemeToggle`, chart and status-tone helpers, a color bridge for canvas renderers |
| **Sign-in** | `createSessionStore` and `LoginCard` for the platform admin server, plus the admin API types |
| **Data** | `request` HTTP client with an in-flight counter, `useAsync`, `usePolling`, `usePagedList` |
| **Tables** | `DataTable`, and `ObjectTable` with the standard toolbar: search, selection, bulk delete, paging |
| **Page parts** | `PageHeader`, `PageTabs`, `MetricTile`, `AccentSurface`, `AppNotice`, `TermList`, `StatusBadge`, `ConfirmDialog` |
| **API tools** | `ApiExplorer` for OpenAPI documents, `ApiTokenAuthFields` for static bearer or OIDC credentials |
| **Assistant** | `AssistantChat`, `AssistantDrawer`, `AssistantMarkdown` and the conversation client behind them |
| **Docs** | `DocsGate` and `openDocsSession` for a signed-in documentation site, with a Starlight config and theme |
| **Primitives** | A self-contained copy of the shadcn-vue primitives the components use |

## Install

```sh
npm install @bboxsys/ui
```

Runtime dependencies are peers, so your app owns the versions:

```sh
npm install vue pinia @vueuse/core @lucide/vue reka-ui clsx \
  class-variance-authority tailwind-merge marked dompurify echarts
```

`@astrojs/starlight` is an optional peer, needed only for a documentation
site.

## Setup

The kit expects Tailwind v4 with `tw-animate-css` and `shadcn-vue` in your
app. In the main stylesheet, let Tailwind scan the kit, then import the
theme and a brand preset:

```css
@import "tailwindcss";
@import "tw-animate-css";
@source "../node_modules/@bboxsys/ui/src";
@import "shadcn-vue/tailwind.css";

@import "@bboxsys/ui/theme.css";
@import "@bboxsys/ui/brands/blue.css";
```

Then import from the package root:

```ts
import { createSessionStore, LoginCard, ObjectTable, request } from '@bboxsys/ui'
```

## Entry points

| Import | Purpose |
|---|---|
| `@bboxsys/ui` | Components, composables, helpers and types |
| `@bboxsys/ui/theme.css` | Theme tokens and Tailwind utilities, light and dark |
| `@bboxsys/ui/brands/blue.css`, `orange.css` | Brand presets |
| `@bboxsys/ui/vite` | Dev-server helpers: `devProxy`, `devEndpoints`, `brandTokens` |
| `@bboxsys/ui/starlight`, `starlight.css` | Starlight site configuration and theme |

## Dev server

In development an app proxies its API to a running backend so the SPA stays
same-origin, as it is when embedded in the server binary:

```ts
// vite.config.ts
import { devEndpoints, devProxy } from '@bboxsys/ui/vite'

const backend = { fallback: 'https://localhost:8880', env: 'MYAPP_UI_SERVER' }

export default defineConfig({
  plugins: [vue(), devEndpoints(backend)],
  server: { proxy: devProxy(['/serviceendpoints'], backend) },
})
```

Two environment variables repoint every app that uses these helpers:

| Variable | Effect |
|---|---|
| `ADMIN_PROXY_TARGET_DEV` | Backend the proxy forwards to; wins over the app's own variable and `fallback` |
| `BBOX_DEV_ENDPOINTS` | Rewrites the service URLs the backend publishes, which decide where the SPA signs in |

```sh
ADMIN_PROXY_TARGET_DEV=https://localhost:9900 npm run dev
```

## Documentation

- [Developer guide](https://github.com/bboxsys/platform/blob/main/ui/docs/DEVELOPER.md):
  every component, prop and helper, with the theme and dev-server details.
- [Documentation sites](https://github.com/bboxsys/platform/blob/main/docs/DOCSITE.md):
  serving a Starlight site behind sign-in.
- [Platform guide](https://github.com/bboxsys/platform/blob/main/docs/DEVELOPER.md):
  the Go side the kit talks to.

## Contributing

```sh
npm install
npm run typecheck
npm test
```

The kit lives in the `ui/` directory of `bboxsys/platform` and is published
to npm at the platform's version on every `v*` tag.

## License

[Apache-2.0](./LICENSE)
