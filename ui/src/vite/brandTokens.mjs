// The product brand, carried from its stylesheet to the surfaces the
// product does not build itself.
//
// A product writes its brand once, as the :root and .dark token blocks of
// src/brand.css (the five brand tokens of a kit preset, plus any chart or
// chrome tokens it overrides). Its SPA imports that file after the kit's
// theme. The platform admin console a product embeds and the product's
// Starlight documentation site are separate builds that cannot import it, so:
//
//   - brandTokens() is a Vite build plugin that emits the token blocks as
//     brand-tokens.css in the SPA's dist/, which the product already embeds;
//     the admin server reads it from there (admin.HostBranding) and serves it
//     to the console as branding_css.
//   - docsSite({ brand }) in @bboxsys/ui/starlight loads the same file into
//     the documentation site, mapped onto Starlight's two themes.
//
// Both go through extractBrandTokens, which keeps only custom property
// declarations in :root and .dark: a font face or an @import by relative URL
// would resolve against the wrong origin elsewhere, so they never travel.

import fs from 'node:fs'
import nodePath from 'node:path'

/** The file the plugin writes into the build output. */
export const BRAND_TOKENS_FILE = 'brand-tokens.css'

/** The stylesheet a product keeps its brand in, relative to the app root. */
export const DEFAULT_BRAND_SOURCE = 'src/brand.css'

const BLOCK_SELECTORS = [':root', '.dark']

function stripComments(css) {
  return css.replace(/\/\*[\s\S]*?\*\//g, '')
}

// Top-level blocks of a stylesheet as [selector, body] pairs, by brace
// matching, so a nested at-rule or a selector list never confuses it.
function topLevelBlocks(css) {
  const blocks = []
  let depth = 0
  let selector = ''
  let body = ''
  for (let i = 0; i < css.length; i++) {
    const ch = css[i]
    if (ch === '{') {
      if (depth === 0) body = ''
      else body += ch
      depth++
    } else if (ch === '}') {
      depth--
      if (depth === 0) {
        blocks.push([selector.trim(), body])
        selector = ''
      } else {
        body += ch
      }
    } else if (depth === 0) {
      selector += ch
      // A statement at-rule (@import ...;) ends without a block.
      if (ch === ';') selector = ''
    } else {
      body += ch
    }
  }
  return blocks
}

/**
 * The custom property declarations of a block body, in order, as
 * [name, value] pairs. Anything that is not `--name: value;` is dropped.
 */
export function declarations(body) {
  const out = []
  const re = /(--[\w-]+)\s*:\s*([^;]+);/g
  let m
  while ((m = re.exec(body)) !== null) {
    const value = m[2].trim()
    if (/\burl\(/i.test(value)) continue
    out.push([m[1], value])
  }
  return out
}

/**
 * Reduce a brand stylesheet to its :root and .dark token blocks, as the text
 * the admin console injects and the documentation site maps. Comments, font
 * faces, imports, utilities and every other rule are left behind. Throws when
 * the stylesheet has no :root block or sets no --brand token, since a brand
 * file without a brand is a wrong path rather than a choice.
 */
export function extractBrandTokens(css) {
  const blocks = new Map(BLOCK_SELECTORS.map(sel => [sel, []]))
  for (const [selector, body] of topLevelBlocks(stripComments(css))) {
    if (!blocks.has(selector)) continue
    blocks.get(selector).push(...declarations(body))
  }
  const root = blocks.get(':root')
  if (root.length === 0) throw new Error('brand stylesheet has no :root token block')
  if (!root.some(([name]) => name === '--brand' || name === '--brand-hue')) {
    throw new Error('brand stylesheet sets neither --brand nor --brand-hue in :root')
  }
  return BLOCK_SELECTORS
    .filter(sel => blocks.get(sel).length > 0)
    .map(sel => `${sel} {\n${blocks.get(sel).map(([n, v]) => `  ${n}: ${v};`).join('\n')}\n}\n`)
    .join('')
}

/**
 * The same tokens for a Starlight site, whose themes are `:root` (dark by
 * default) and `:root[data-theme='light']` rather than `.dark` and `:root`:
 * the light block is the stylesheet's :root, the dark block its :root with
 * .dark layered over it. The kit's Starlight theme derives its accents from
 * --brand-hue and paints charts from --chart-*, so both carry.
 */
export function starlightBrandCSS(css) {
  const tokens = extractBrandTokens(css)
  const blocks = new Map(topLevelBlocks(tokens))
  const light = declarations(blocks.get(':root') ?? '')
  const dark = new Map(light)
  for (const [n, v] of declarations(blocks.get('.dark') ?? '')) dark.set(n, v)
  const render = (sel, decls) => `${sel} {\n${[...decls].map(([n, v]) => `  ${n}: ${v};`).join('\n')}\n}\n`
  return render(':root', dark) + render(":root[data-theme='light']", light)
}

/** Read and reduce a brand stylesheet on disk. */
export function readBrandTokens(file, root = process.cwd()) {
  const path = nodePath.resolve(root, file)
  let css
  try {
    css = fs.readFileSync(path, 'utf8')
  } catch (err) {
    throw new Error(`brand stylesheet ${path}: ${err.message}`)
  }
  try {
    return extractBrandTokens(css)
  } catch (err) {
    throw new Error(`${path}: ${err.message}`)
  }
}

/**
 * Vite build plugin that writes the brand token blocks of `source` (default
 * src/brand.css) to brand-tokens.css in the build output, next to index.html,
 * so a product that embeds dist/ carries its brand for the admin console:
 *
 *   plugins: [vue(), tailwindcss(), brandTokens()]
 *
 * A product on a kit preset (blue, orange) does not use it; it names the
 * preset as branding_brand instead.
 */
export function brandTokens(source = DEFAULT_BRAND_SOURCE, opts = {}) {
  const { fileName = BRAND_TOKENS_FILE } = opts
  let root = process.cwd()
  return {
    name: '@bboxsys/ui:brand-tokens',
    apply: 'build',
    configResolved(config) {
      root = config.root
    },
    generateBundle() {
      this.emitFile({ type: 'asset', fileName, source: readBrandTokens(source, root) })
    },
  }
}
