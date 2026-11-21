import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { request } from '@bboxsys/ui'
import blueCss from '@bboxsys/ui/brands/blue.css?raw'
import orangeCss from '@bboxsys/ui/brands/orange.css?raw'
import { adminBase } from './session'

export interface Branding {
  name: string
  logoUrl?: string
  // A @bboxsys/ui brand preset name; blue when absent or unknown.
  brand?: string
  // A host's own brand token block, injected after the console's stylesheet.
  css?: string
}

// Branding is served unauthenticated by the admin server so the host
// application (any product) controls the console's name, mark and
// brand. Defaults to Black Box until the endpoint responds.
const DEFAULT_NAME = 'Black Box'

// The kit presets, as the stylesheet text each one is. The console's own
// stylesheet imports the theme with no preset (blue), so applying a brand is
// appending the chosen preset after it, where it wins the cascade.
const PRESETS: Record<string, string> = { blue: blueCss, orange: orangeCss }
const DEFAULT_BRAND = 'blue'

// The brand of the last response, so a reload paints in the host's brand
// before the bootstrap answers instead of flashing blue first.
const CACHE_KEY = 'admin.branding.brand'
const STYLE_ID = 'host-brand'

function applyBrand(b: Pick<Branding, 'brand' | 'css'>): void {
  if (typeof document === 'undefined') return
  const preset = b.brand && PRESETS[b.brand] ? b.brand : DEFAULT_BRAND
  const css = b.css || (preset === DEFAULT_BRAND ? '' : PRESETS[preset])
  let style = document.getElementById(STYLE_ID) as HTMLStyleElement | null
  if (!style) {
    style = document.createElement('style')
    style.id = STYLE_ID
    document.head.appendChild(style)
  }
  style.textContent = css
  // The canvas bridge in the kit re-resolves its tokens when this changes.
  document.documentElement.dataset.brand = b.css ? 'custom' : preset
}

function cachedBrand(): Pick<Branding, 'brand' | 'css'> | null {
  try {
    const raw = localStorage.getItem(CACHE_KEY)
    return raw ? JSON.parse(raw) : null
  } catch {
    return null
  }
}

function cacheBrand(b: Pick<Branding, 'brand' | 'css'>): void {
  try {
    localStorage.setItem(CACHE_KEY, JSON.stringify(b))
  } catch {
    // storage unavailable: the brand still applies for this load
  }
}

export const useBrandingStore = defineStore('branding', () => {
  const name = ref(DEFAULT_NAME)
  const logoUrl = ref('')
  const brand = ref(DEFAULT_BRAND)
  const loaded = ref(false)

  // True when the host set no custom brand. The bundled Black Box logo is the
  // fallback mark only in this case; any custom brand uses its logoUrl or a
  // name-derived monogram, never the Black Box logo.
  const isDefault = computed(() => name.value === DEFAULT_NAME)

  // Monogram fallback used when the host provides no logo image.
  const monogram = computed(() => {
    const parts = name.value.trim().split(/\s+/).filter(Boolean)
    const letters = parts.length > 1 ? parts[0][0] + parts[1][0] : parts[0]?.slice(0, 2) ?? '?'
    return letters.toUpperCase()
  })

  function documentTitle(pageTitle?: string): string {
    return pageTitle ? `${pageTitle} - ${name.value} Admin` : `${name.value} Admin`
  }

  function setBrand(b: Pick<Branding, 'brand' | 'css'>): void {
    applyBrand(b)
    brand.value = b.css ? 'custom' : (b.brand && PRESETS[b.brand] ? b.brand : DEFAULT_BRAND)
  }

  async function load() {
    if (loaded.value) return
    const cached = cachedBrand()
    if (cached) setBrand(cached)
    try {
      const b = await request<Branding>(`${adminBase()}/branding/`)
      if (b?.name) name.value = b.name
      logoUrl.value = b?.logoUrl ?? ''
      const next = { brand: b?.brand, css: b?.css }
      setBrand(next)
      cacheBrand(next)
    } catch {
      // keep defaults; the console still works under the Black Box brand
    } finally {
      loaded.value = true
    }
  }

  return { name, logoUrl, brand, isDefault, monogram, documentTitle, load }
})
