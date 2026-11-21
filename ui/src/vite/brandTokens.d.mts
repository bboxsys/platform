/** The file brandTokens() writes into the build output: 'brand-tokens.css'. */
export declare const BRAND_TOKENS_FILE: 'brand-tokens.css'

/** The stylesheet a product keeps its brand in: 'src/brand.css'. */
export declare const DEFAULT_BRAND_SOURCE: 'src/brand.css'

/** The custom property declarations of a block body, in order; url() values are dropped. */
export declare function declarations(body: string): [string, string][]

/**
 * Reduce a brand stylesheet to its :root and .dark custom property blocks.
 * Throws when there is no :root block or it sets neither --brand nor
 * --brand-hue.
 */
export declare function extractBrandTokens(css: string): string

/** The same tokens mapped onto Starlight's :root (dark) and :root[data-theme='light']. */
export declare function starlightBrandCSS(css: string): string

/** Read a brand stylesheet relative to root (default: the working directory) and reduce it. */
export declare function readBrandTokens(file: string, root?: string): string

export interface BrandTokensOptions {
  /** Output file name. Defaults to 'brand-tokens.css'. */
  fileName?: string
}

/**
 * Vite build plugin that emits the brand token blocks of `source` (default
 * 'src/brand.css') as brand-tokens.css in the build output, for the admin
 * console the product embeds (admin.HostBranding reads it from the SPA's dist).
 */
export declare function brandTokens(source?: string, opts?: BrandTokensOptions): {
  name: string
  apply: 'build'
  configResolved: (config: any) => void
  generateBundle: (this: any) => void
}
