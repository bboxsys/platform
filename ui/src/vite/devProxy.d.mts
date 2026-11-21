export declare const DEV_TARGET_ENV: 'ADMIN_PROXY_TARGET_DEV'

export interface DevProxyOptions {
  /** Target used when no environment variable is set. */
  fallback?: string
  /** Name of an app-specific variable checked after the shared override. */
  env?: string
}

export interface DevProxyEntry {
  target: string
  secure: boolean
  changeOrigin: boolean
}

export declare function devProxyTarget(opts?: DevProxyOptions): string

export declare function devProxy(
  paths: readonly string[],
  opts?: DevProxyOptions,
): Record<string, DevProxyEntry>

export declare const DEV_ENDPOINTS_ENV: 'BBOX_DEV_ENDPOINTS'

export interface DevEndpointsOptions extends DevProxyOptions {
  /** Route the endpoint map is served on. Defaults to '/serviceendpoints'. */
  route?: string
  /** Project name; reads ~/.config/{project}-test/dev-endpoints.json. */
  project?: string
}

/** Parse the BBOX_DEV_ENDPOINTS syntax into a {kind: url} map. */
export declare function parseDevEndpoints(raw?: string): Record<string, string>

/** Vite plugin that applies dev-only overrides to the service endpoint map. */
export declare function devEndpoints(opts?: DevEndpointsOptions): {
  name: string
  apply: 'serve'
  configureServer: (server: any) => void
}

export interface WatchLinkedKitOptions {
  /** Package to follow. Defaults to '@bboxsys/ui'. */
  packageName?: string
  /** Directory inside the package to watch. Defaults to 'src'. */
  dir?: string
}

/** Map a file under realDir to its path under linkedDir, or null when outside. */
export declare function linkedPath(realDir: string, linkedDir: string, file: string): string | null

/** Vite plugin that hot-reloads the kit when it is linked by path (file:). */
export declare function watchLinkedKit(opts?: WatchLinkedKitOptions): {
  name: string
  apply: 'serve'
  configureServer: (server: any) => void
}

// The brand token plugin, re-exported from brandTokens.mjs (types in brandTokens.d.mts).
export {
  BRAND_TOKENS_FILE, DEFAULT_BRAND_SOURCE, brandTokens, extractBrandTokens, readBrandTokens, starlightBrandCSS,
} from './brandTokens.mjs'
export type { BrandTokensOptions } from './brandTokens.mjs'
