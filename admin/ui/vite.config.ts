import path from 'node:path'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { devEndpoints, devProxy, watchLinkedKit } from '@bboxsys/ui/vite'
import { defineConfig } from 'vite'

// Shared by the endpoint-map plugin and the proxy so both follow the same
// target: ADMIN_PROXY_TARGET_DEV, then ADMIN_PROXY_TARGET, then the default.
// 127.0.0.1 rather than localhost so the admin's request log records the
// proxy's IPv4 loopback address instead of ::1.
const devServer = { fallback: 'https://127.0.0.1:8901', env: 'ADMIN_PROXY_TARGET', project: 'admin' }

export default defineConfig({
  plugins: [vue(), tailwindcss(), devEndpoints(devServer), watchLinkedKit()],
  resolve: {
    alias: {
      '@': path.resolve(import.meta.dirname, './src'),
    },
    // @bboxsys/ui is a file: dependency symlinked to ../../ui. Resolve it
    // through the symlink so the kit's imports (vue, pinia, reka-ui, ...) and
    // Vue's SFC type resolution use this app's node_modules; the kit itself
    // needs no node_modules (fresh clones, CI, consumers that build this SPA
    // from a platform checkout). tsconfig.app.json sets preserveSymlinks too.
    preserveSymlinks: true,
    // Belt and braces: if ../../ui happens to have its own node_modules (after
    // running the kit's tests), never bundle a second copy of these.
    dedupe: ['vue', 'pinia', '@vueuse/core', 'reka-ui', '@lucide/vue', 'class-variance-authority', 'clsx', 'tailwind-merge', 'leaflet'],
  },
  optimizeDeps: {
    // Shipped as raw .vue/.ts source; the dev pre-bundler must not touch it.
    exclude: ['@bboxsys/ui'],
  },
  server: {
    port: 4400,
    // Edits under ../../ui reach the running server through watchLinkedKit():
    // the watcher ignores node_modules and does not follow the kit's symlink.
    // In development the API is proxied to a running admin server so the SPA
    // stays same-origin, exactly as it is when embedded in the binary. The
    // target is ADMIN_PROXY_TARGET_DEV (shared by every app using the kit),
    // then ADMIN_PROXY_TARGET, then the admin server's default port.
    proxy: devProxy(['/api', '/serviceendpoints', '/jwt'], devServer),
  },
})
