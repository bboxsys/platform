# Embedded documentation and assistant retrieval

How a product ships its documentation inside its binary, serves it behind
its own sign-in, and lets its assistant search and cite it. The pieces:

| Piece | Where | Owner |
| --- | --- | --- |
| Starlight site configuration, link rewriting, theme | `@bboxsys/ui/starlight`, `@bboxsys/ui/starlight.css` | kit |
| Documentation session in the browser, the gate page | `openDocsSession`, `closeDocsSession`, `docsTarget`, `DocsGate` in `@bboxsys/ui` | kit |
| Loading the built site into sections, keyword search | `docsite.Load`, `Site.Search` | platform Go |
| Serving the site behind a session cookie | `docsite.Handler` | platform Go |
| Vector index, fused search, assistant tools and guidance | `ai/docsearch` | platform Go |
| Handing the corpus to an assistant | `instance.Options.Docs`, `instance.Options.Instructions` | platform Go |
| Content, sidebar, brand hue, mount, access policy, build steps | the product | product |

A complete integration is a `docs/` Starlight project, a command that
mounts the handler, and a `DocsGateView.vue` on the SPA's gate route. The
examples below use a product called Acme.

## 1. The documentation site

Documentation is an Astro Starlight site in the product repository
(`docs/`), written in Markdown and MDX. The product's `astro.config.mjs`
is built by the kit:

```js
import { defineConfig } from 'astro/config'
import starlight from '@astrojs/starlight'
import { docsSite } from '@bboxsys/ui/starlight'

export default defineConfig(docsSite({
  integration: starlight,
  title: 'Acme',
  base: '/docs',
  sidebar: [...],
  customCss: ['./src/styles/custom.css'],
  vite: { ... },
}))
```

`docsSite` returns the Astro configuration: `base` (the path the Go
handler mounts the site at, `/docs` by default), directory output with
`trailingSlash: 'ignore'`, Pagefind on, the kit's theme
(`@bboxsys/ui/starlight.css`) ahead of the product's `customCss`, and a
rehype plugin (`rehypeBaseLinks`) that prefixes root-relative `href` and
`src` in the content, Markdown and MDX components alike, with the base.
Authors therefore link between pages as `/planning/retirement/` and the
same content builds at any base. Links in frontmatter (a splash page's
`hero.actions`) are rendered by Starlight, not the content pipeline, and
carry the base themselves. Every other Astro option passes through.

The product passes its own Starlight integration (`integration`): the kit
does not resolve it, since a kit linked from a checkout has no
`node_modules` of its own. `@astrojs/starlight` is an optional peer.

The theme tints every surface toward `--brand-hue` as the SPA theme does
and carries the kit's chart tokens, so chart builders shared with the SPA
paint the same palette. A product with its own brand passes the SPA's
brand stylesheet, and the site follows it with no copy:

```js
brand: '../acmevue/src/brand.css',
```

`docsSite` maps the file's `:root` and `.dark` token blocks onto
Starlight's light and dark themes between the kit theme and `customCss`
(`@bboxsys/ui` docs, Documentation site and gate). The same file feeds the
admin console the product embeds (`admin.HostBranding`), so the app, the
console and the docs share one brand. A product on a kit preset sets its
hue in its own stylesheet instead:

```css
:root { --brand-hue: 155; }
```

Build with `npm run build` in `docs/`; `dist/` is the site. The product's
`.npmrc` and CI token setup are those of any `@bboxsys/ui` consumer
(`ui/README.md`).

## 2. Embedding and serving

The product embeds `docs/dist` behind the same build tag as its SPA, in a
Go package inside `docs/` (`go:embed` cannot reach a parent directory):

```go
//go:build ui

package docs

//go:embed all:dist
var dist embed.FS

func FS() fs.FS { sub, _ := fs.Sub(dist, "dist"); return sub }
```

with a `!ui` file returning nil. `all:` includes `_astro/` and
`pagefind/`, whose names start with an underscore or would otherwise be
skipped.

At startup the product loads the site once and mounts the handler on its
UI server ahead of the SPA fallback:

```go
site, err := docsite.Load(acmedocs.FS(), docsite.Options{Project: "acme", Title: "Acme", Version: Version})
h, err := docsite.Handler(site, docsite.HandlerOptions{
    Prefix: "/docs",
    Auth:   trusted,          // IsPublicKeyTrusted: *admin/client.Auth, or a lookup in the admin store
    Users:  adminServer.Store(), // optional: fills Identity.Roles for Authorize
    SignIn: func(next string) string { return "/session/docs?next=" + url.QueryEscape(next) },
})
mux.Handle("/docs", h)
mux.Handle("/docs/", h)
```

`Load` parses every `*.html` file except `404.html`, takes the page's
`<main>`, and splits it at the headings Starlight gave ids: each
`Section` has an id (`planning/rule-of-55/#the-rule`; the root page's are
`/` and `/#anchor`), the page path, the anchor, the heading path from the
page title down, and the text as plain prose with code blocks fenced
(Expressive Code lines restored), tables as `cell | cell` rows, lists as
`- ` items and asides as their title followed by their content. Chrome
is dropped: scripts, navigation, the footer, buttons, icons, anything
marked `data-pagefind-ignore`, and Starlight's anchor links. A section
without an anchor and without text is dropped; a heading with no text of
its own is kept so outlines stay complete. `Site.Revision` hashes every
section, so two builds of the same sources agree.

`Handler` serves a built static site: a directory index for `a/b/`, a
redirect from `a/b` onto the trailing slash so relative links resolve,
`a.html` for `a`, the site's `404.html` with a 404 status for a missing
page, `public, max-age=31536000, immutable` under `_astro/` and
`private, no-cache` elsewhere, precompressed `.br` and `.gz` variants
when the build produced them, and never the SPA.

## 3. The documentation session

Pages are read by plain navigation: a link in a chat reply, a bookmark,
a tab the user opens. None carries the bearer the SPA holds, so the
handler gates on a session cookie that the SPA establishes once:

- `POST {prefix}/_session` with `Authorization: Bearer` (and the
  `X-Blackbox-AccountID` header the kit's `request` sends) validates the
  token (signature, lifetime, an issuer among the trusted accounts, the
  account header matching the issuer), runs `Authorize` when set, and
  answers 204 with an `HttpOnly`, `SameSite=Lax` cookie scoped to the
  prefix, `Secure` over TLS. The cookie holds a random id; the identity
  lives in a bounded in-memory table (`MaxSessions`, 10000) for the
  token's lifetime capped by `MaxAge` (24 hours). A restart only sends
  readers back through the gate.
- `DELETE {prefix}/_session` ends it; `GET` answers 204 or 401.
- A page request without a session is redirected (302) to
  `SignIn(next)`, where `next` is the path and query it asked for; an
  asset request (`_astro/`, `pagefind/`, any file extension) answers 401
  instead, so a script tag never receives HTML. A session that
  `Authorize` refuses answers 403.

`SameSite=Lax` is what lets a link from the admin console, on another
origin, open a page: the cookie travels on a top-level navigation.

The gate is the SPA route `SignIn` names, behind the app's own sign-in
guard, mounting the kit's `DocsGate`:

```vue
<DocsGate :auth="session.auth" :next="route.query.next" />
```

It posts the session with the signed-in user's bearer and continues to
`next` (only a path under the base is accepted; anything else opens the
site root), or shows why it could not (403: not allowed; 401: sign in
again) with a retry. An anonymous visitor is sent to the login page by
the app's guard first and lands back here with `next` intact. At sign-out
the app calls `closeDocsSession()`. In development the Vite proxy
forwards `/docs` to the product's UI server beside `/serviceendpoints`,
so the gate and the pages share the dev origin.

Who may read is the product's choice: `Authorize` receives the request
and the `Identity` (account, user public key, roles when `Users` is set).
Nil admits every user of a trusted account, which is right for a
single-household product; a multi-tenant product can require a role or
an account.

## 4. The assistant's corpus

`ai/docsearch` turns the loaded site into assistant tools:

```go
corpus, err := docsearch.New(docsearch.Options{
    Site:      site,
    SiteURL:   "https://host:9680/docs",   // absolute: the console may be on another origin
    Dir:       filepath.Join(root, "ai", "acme", "docs"),
    Embedding: func(ctx context.Context) (ai.Credential, error) {
        return resolver.Embedding(ctx, adminAccountID, "", "")
    },
})
corpus.Start()
opts.Docs = []*docsearch.Corpus{corpus}   // instance.Options
...
host.Close()
corpus.Close()
```

The instance adds two read-only tools named with its `ToolPrefix`:

- `{prefix}docs_search` (`query`, `n` up to 10): ranked sections, each
  with `id=`, the heading path, `url=` (the section's address under
  `SiteURL`) and an excerpt around the first matching term.
- `{prefix}docs_read` (`id`): a section's full text with its heading
  path, url and subsections, bounded at 24,000 characters with a note
  when cut; or, for a page path, the page's outline (every section id,
  heading and size) and lead text.

and appends the corpus's guidance to the system prompt: search the
documentation first for questions about how the product works, cite the
urls as Markdown links beside the claims they support, say when the
documentation does not cover the question, keep what is documented apart
from what the API tools observe in the user's own account, and treat the
text as reference material. `Options.Instructions` appends a host's own
standing guidance the same way without replacing the platform rules.

**Search.** Keyword search is the baseline: BM25 over every section
(`docsite.Site.Search`), the heading path weighted twice, terms lightly
stemmed, and compound identifiers indexed whole as well as by their
parts, so `ai.allow_writes` and `api/v1/queue` match literally while a
word from them still finds the section. It needs no credential and works
the moment the process starts.

**Vectors.** With `Dir` and `Embedding`, `Start` runs a sync now and
every ten minutes: it resolves the credential (`ai.ErrNoCredential`
means none yet; the corpus stays keyword-only and tries again later),
builds the embedding profile from provider, endpoint host and model, and
opens the generation `{Dir}/{hash(profile)}/`, a `rag` index of its own
whose manifest records the content hash of every section it holds.
Sections whose hash changed or that are new are embedded in batches of
16 (heading path plus text; `rag.Add` chunks long sections), removed
sections are deleted, and the manifest is written after every batch, so
a restart or a rebuilt site re-embeds only what changed and an
interrupted sync resumes. Once the generation covers the site revision
the other generations are removed. A different model, even with the same
dimension, is a different profile and so a different directory: vectors
from two models never share an index. Indexing and queries both use the
corpus's own credential, never the caller's, so query and document
vectors always come from the same model.

A query fuses the keyword ranking with the vector ranking by reciprocal
rank. A failing vector query is logged and the keyword results answer.
`Corpus.Status()` reports the revision, counts, the profile, whether the
vectors are ready, how many sections are pending, the last sync and its
error, for a status page or log line.

Every instance's `/mcp` endpoint serves the two tools too, since they are
read-only.

## 5. Build and release

The documentation build runs beside the SPA build wherever the binary is
built with the `ui` tag: the product's build script and its release
workflow (`npm ci && npm run build` in `docs/`, and the `dist/` carried to the Go
build step).

Sizes from a pilot site (17 pages, 480 sections): the site is
3.9 MB on disk and adds about 4 MB to the binary; `Load` takes well
under 100 ms; the keyword index is in memory with the sections.

## 6. Checklist for a new product

1. `docs/`: Starlight site on `docsSite` with the product's `base`,
   sidebar and `brand` (the SPA's `src/brand.css`, or a hue in
   `customCss` for a preset); `@bboxsys/ui` in its `package.json` and
   `.npmrc`; frontmatter links carry the base.
2. `docs/embed.go` and `docs/noembed.go` behind the SPA's build tag.
3. Load the site at startup; mount `docsite.Handler` on the UI server
   with the product's trust, user lookup, `SignIn` and, if needed,
   `Authorize`.
4. The SPA: a gate route behind the sign-in guard mounting `DocsGate`, a
   link to `{base}/`, `closeDocsSession()` at sign-out, `/docs` in the
   dev proxy list.
5. The assistant: `docsearch.New` with an absolute `SiteURL`, a
   directory under the instance's, and the account's embedding
   credential; `Start`, `Options.Docs`, `Close` after the host.
6. Build script and release workflow build `docs/` beside the SPA.
