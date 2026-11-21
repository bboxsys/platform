export declare const DEFAULT_DOCS_BASE: '/docs'
export declare const THEME_CSS: '@bboxsys/ui/starlight.css'

/** The virtual stylesheet id the product's brand is served under when `brand` is set. */
export declare const BRAND_CSS_ID: 'virtual:bboxsys-brand.css'

/** Vite plugin serving BRAND_CSS_ID from the brand stylesheet at `file`, mapped onto Starlight's themes. */
export declare function brandCSSPlugin(file: string): {
  name: string
  configResolved: (config: any) => void
  resolveId: (id: string) => string | null
  load: (this: any, id: string) => string | null
}

/** Prefix a root-relative URL with the base; other URLs are unchanged. */
export declare function withBase(url: string, base?: string): string

export interface RehypeBaseLinksOptions {
  base?: string
}

/** Rehype plugin that rewrites root-relative links in content onto the base. */
export declare function rehypeBaseLinks(options?: RehypeBaseLinksOptions): (tree: unknown) => void

export interface DocsSiteOptions {
  /** Site title, shown in the header and used by the assistant. */
  title: string
  description?: string
  /** Mount path of the site in the product binary. Defaults to '/docs'. */
  base?: string
  /** Starlight sidebar configuration. */
  sidebar?: unknown[]
  logo?: Record<string, unknown>
  favicon?: string
  social?: unknown[]
  /**
   * The product's brand stylesheet (its SPA's src/brand.css, relative to the
   * Astro root), whose :root and .dark tokens are mapped onto the site's
   * themes between the kit's theme and customCss, so the docs follow the
   * app's brand without a copy.
   */
  brand?: string
  /** Product stylesheets, applied after the kit's theme and the brand. */
  customCss?: string[]
  /** Further Starlight options, merged over the ones above. */
  starlight?: Record<string, unknown>
  /** The product's Starlight integration (`import starlight from '@astrojs/starlight'`). */
  integration: (options: Record<string, unknown>) => unknown
  /** Astro markdown options; the base-link plugin is prepended to rehypePlugins. */
  markdown?: Record<string, unknown> & { rehypePlugins?: unknown[] }
  /** Further Astro integrations, after Starlight. */
  integrations?: unknown[]
  build?: Record<string, unknown>
  /** Any other Astro option (vite, server, ...) is passed through. */
  [key: string]: unknown
}

/** Build the Astro configuration for a product documentation site. */
export declare function docsSite(options: DocsSiteOptions): Record<string, unknown>
