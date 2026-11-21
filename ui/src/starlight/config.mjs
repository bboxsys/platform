// Astro and Starlight configuration for a product's documentation site.
//
// astro.config.mjs is loaded by Node, not by the app's bundler, so this file
// is plain ESM JavaScript like ../vite/devProxy.mjs; types are in
// config.d.mts. The Go half, which serves the built site behind the
// product's sign-in and reads its sections for the assistant, is the
// platform's docsite package (docs/DOCSITE.md in the platform repository).
//
// A product's astro.config.mjs becomes:
//
//   import { defineConfig } from 'astro/config'
//   import starlight from '@astrojs/starlight'
//   import { docsSite } from '@bboxsys/ui/starlight'
//
//   export default defineConfig(docsSite({
//     integration: starlight,
//     title: 'Acme',
//     base: '/docs',
//     sidebar: [...],
//     customCss: ['./src/styles/custom.css'],
//   }))
//
// The product passes the Starlight integration it installed: resolving it
// from here would fail when the kit is linked from a checkout, since Node
// and Vite search node_modules from the kit's real path.
//
// which sets the base path the Go handler mounts the site at, keeps
// Starlight's directory output and Pagefind search, applies the kit's
// theme before the product's own stylesheet, and rewrites root-relative
// links in the content onto the base, so pages keep linking to each other
// as "/planning/retirement/".

import fs from 'node:fs'
import nodePath from 'node:path'
import { starlightBrandCSS } from '../vite/brandTokens.mjs'

export const DEFAULT_DOCS_BASE = '/docs'

/** The kit's Starlight theme, resolved by Vite through the package exports. */
export const THEME_CSS = '@bboxsys/ui/starlight.css'

/**
 * The module id the product's brand is served under when docsSite is given
 * a `brand` stylesheet: a virtual stylesheet Vite resolves through the
 * plugin below, so the SPA's src/brand.css is read in place and mapped onto
 * Starlight's themes without a copy in the docs tree.
 */
export const BRAND_CSS_ID = 'virtual:bboxsys-brand.css'

/**
 * Vite plugin serving BRAND_CSS_ID as the Starlight mapping of the brand
 * stylesheet at `file` (resolved against the Astro root at config time). The
 * file is read on each load, so the dev server follows edits.
 */
export function brandCSSPlugin(file) {
  let root = process.cwd()
  return {
    name: '@bboxsys/ui:docs-brand',
    configResolved(config) {
      root = config.root
    },
    resolveId(id) {
      return id === BRAND_CSS_ID ? BRAND_CSS_ID : null
    },
    load(id) {
      if (id !== BRAND_CSS_ID) return null
      const path = nodePath.resolve(root, file)
      this.addWatchFile?.(path)
      let css
      try {
        css = fs.readFileSync(path, 'utf8')
      } catch (err) {
        throw new Error(`docsSite brand stylesheet ${path}: ${err.message}`)
      }
      return starlightBrandCSS(css)
    },
  }
}

function normalizeBase(base) {
  const trimmed = String(base ?? DEFAULT_DOCS_BASE).replace(/^\/+|\/+$/g, '')
  return trimmed ? `/${trimmed}` : '/'
}

/**
 * Rewrite a root-relative URL onto the base. Anything else (absolute,
 * relative, fragment-only, already under the base) is returned unchanged.
 */
export function withBase(url, base = DEFAULT_DOCS_BASE) {
  const b = normalizeBase(base)
  if (typeof url !== 'string' || b === '/') return url
  if (!url.startsWith('/') || url.startsWith('//')) return url
  if (url === b || url.startsWith(`${b}/`) || url.startsWith(`${b}#`) || url.startsWith(`${b}?`)) return url
  return `${b}${url}`
}

const URL_ATTRIBUTES = new Set(['href', 'src', 'link', 'poster'])

/**
 * Rehype plugin: prefix root-relative href and src attributes with the base,
 * in HTML elements and in MDX component attributes (LinkCard href, for
 * example). Starlight applies the base to its own navigation but leaves
 * content links alone; this closes the gap so authors write site-root
 * paths and the same content builds at any base.
 */
export function rehypeBaseLinks(options = {}) {
  const base = normalizeBase(options.base)
  const rewrite = (node) => {
    if (node.type === 'element' && node.properties) {
      for (const name of URL_ATTRIBUTES) {
        const v = node.properties[name]
        if (typeof v === 'string') node.properties[name] = withBase(v, base)
      }
    } else if ((node.type === 'mdxJsxFlowElement' || node.type === 'mdxJsxTextElement') && Array.isArray(node.attributes)) {
      for (const a of node.attributes) {
        if (a && a.type === 'mdxJsxAttribute' && URL_ATTRIBUTES.has(a.name) && typeof a.value === 'string') {
          a.value = withBase(a.value, base)
        }
      }
    }
    if (Array.isArray(node.children)) node.children.forEach(rewrite)
  }
  return (tree) => {
    if (base !== '/') rewrite(tree)
  }
}

/**
 * Build the Astro configuration object for a documentation site around the
 * product's Starlight integration.
 */
export function docsSite(options = {}) {
  const {
    title,
    description,
    base = DEFAULT_DOCS_BASE,
    sidebar,
    logo,
    favicon,
    social,
    customCss = [],
    brand,
    starlight: extra = {},
    integration,
    markdown = {},
    integrations = [],
    build = {},
    vite = {},
    ...astro
  } = options
  if (!title) throw new Error('docsSite: title is required')
  if (typeof integration !== 'function') throw new Error('docsSite: integration is required (import starlight from "@astrojs/starlight")')
  const starlight = integration
  const normalizedBase = normalizeBase(base)
  const starlightOptions = {
    title,
    pagefind: true,
    ...(description !== undefined ? { description } : {}),
    ...(logo !== undefined ? { logo } : {}),
    ...(favicon !== undefined ? { favicon } : {}),
    ...(social !== undefined ? { social } : {}),
    ...(sidebar !== undefined ? { sidebar } : {}),
    ...extra,
    // The kit's theme, the product's brand mapped onto it, then the
    // product's own stylesheets.
    customCss: [THEME_CSS, ...(brand ? [BRAND_CSS_ID] : []), ...customCss, ...(extra.customCss ?? [])],
  }
  return {
    ...astro,
    base: normalizedBase,
    trailingSlash: 'ignore',
    build: { format: 'directory', ...build },
    markdown: {
      ...markdown,
      rehypePlugins: [[rehypeBaseLinks, { base: normalizedBase }], ...(markdown.rehypePlugins ?? [])],
    },
    vite: brand ? { ...vite, plugins: [brandCSSPlugin(brand), ...(vite.plugins ?? [])] } : vite,
    integrations: [starlight(starlightOptions), ...integrations],
  }
}
