// The bridge between the CSS token layer and the code that cannot use it.
//
// theme.css is the single definition of every color. DOM elements read it
// directly, through Tailwind utilities or var(). Canvas renderers cannot:
// echarts, cytoscape and pixi paint into a bitmap and need a concrete color
// string, so left alone they grow their own hardcoded hexes that never follow
// a theme change.
//
// This module resolves a token to a hex string those renderers accept, and
// invalidates on a theme switch, so changing a value in the stylesheet changes
// the whole product including the graphs.
import { computed, ref, type Ref } from 'vue'

// Bumped whenever the resolved values could have changed, so token() results
// recompute. Watch this, not the dark flag, from anything that builds a chart
// or graph stylesheet: the cache is already cleared when it fires.
export const themeEpoch: Ref<number> = ref(0)

let observer: MutationObserver | null = null

// The class that flips the palette and the data-brand attribute a host-branded
// console sets both land on <html>, so one observer on its attributes catches
// every theme change in the app.
function watchTheme(): void {
  if (observer || typeof document === 'undefined' || typeof MutationObserver === 'undefined') return
  observer = new MutationObserver(() => {
    cache.clear()
    themeEpoch.value++
  })
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ['class', 'data-brand'] })
}

// What the cache was filled under: the class list plus the brand, since
// either changes every resolved value.
function themeKey(root: HTMLElement): string {
  return `${root.className}|${root.dataset.brand ?? ''}`
}

const cache = new Map<string, string>()
// The theme key the cache was filled under. A watcher on the dark flag can
// run before the observer above has cleared the cache, so validity is decided
// by the key at read time rather than by the observer.
let cacheKey = ''

// oklch -> sRGB hex. Doing the conversion here rather than asking the browser
// keeps the result identical across engines and makes it testable; canvas
// support for an oklch fillStyle is also newer than some of the browsers this
// ships to.
export function oklchToHex(l: number, c: number, hDeg: number): string {
  const h = hDeg * Math.PI / 180
  const A = c * Math.cos(h)
  const B = c * Math.sin(h)

  const lp = l + 0.3963377774 * A + 0.2158037573 * B
  const mp = l - 0.1055613458 * A - 0.0638541728 * B
  const sp = l - 0.0894841775 * A - 1.2914855480 * B

  const L = lp * lp * lp
  const M = mp * mp * mp
  const S = sp * sp * sp

  const r =  4.0767416621 * L - 3.3077115913 * M + 0.2309699292 * S
  const g = -1.2684380046 * L + 2.6097574011 * M - 0.3413193965 * S
  const b = -0.0041960863 * L - 0.7034186147 * M + 1.7076147010 * S

  const encode = (channel: number): string => {
    const linear = channel <= 0.0031308 ? channel * 12.92 : 1.055 * Math.pow(channel, 1 / 2.4) - 0.055
    const byte   = Math.round(Math.min(1, Math.max(0, linear)) * 255)
    return byte.toString(16).padStart(2, '0')
  }
  return `#${encode(r)}${encode(g)}${encode(b)}`
}

// A computed custom property is NOT the text written in the stylesheet: an
// engine may normalize it, and for oklch that means a percentage lightness
// and a bare-dot chroma -- `oklch(55.3% .247 262)` for what the sheet spells
// `oklch(0.553 0.247 262)`. Parse both, plus a literal hex and the rgb() some
// engines hand back, and treat anything else as already usable.
const NUM = String.raw`([+-]?[\d.]+)(%?)`
const OKLCH = new RegExp(String.raw`^oklch\(\s*${NUM}\s+${NUM}\s+${NUM}(?:deg)?`, 'i')
const RGB = /^rgba?\(\s*([\d.]+)[\s,]+([\d.]+)[\s,]+([\d.]+)/i

// The hex form of a CSS color value, for the forms a token can take.
export function colorToHex(value: string): string {
  const text = value.trim()
  if (text.startsWith('#')) return text

  const rgb = text.match(RGB)
  if (rgb) {
    const byte = (n: string) => Math.round(Number(n)).toString(16).padStart(2, '0')
    return `#${byte(rgb[1])}${byte(rgb[2])}${byte(rgb[3])}`
  }

  const m = text.match(OKLCH)
  if (!m) return text
  // Percentages: lightness is 0-100, and chroma's 100% is 0.4 per CSS Color 4.
  const l = m[2] ? Number(m[1]) / 100 : Number(m[1])
  const c = m[4] ? Number(m[3]) * 0.4 / 100 : Number(m[3])
  const h = Number(m[5])
  if (!Number.isFinite(l) || !Number.isFinite(c) || !Number.isFinite(h)) return text
  return oklchToHex(l, c, h)
}

// The resolved value of a CSS custom property, as a hex string. `--` prefix
// optional. Falls back to the supplied color when the token is missing, which
// happens in unit tests and during the first paint of a server-rendered page.
export function token(name: string, fallback = '#888888'): string {
  const key = name.startsWith('--') ? name : `--${name}`
  if (typeof document === 'undefined') return fallback
  watchTheme()

  const root = document.documentElement
  const current = themeKey(root)
  if (current !== cacheKey) {
    cache.clear()
    cacheKey = current
  }
  const hit = cache.get(key)
  if (hit) return hit

  const raw = getComputedStyle(root).getPropertyValue(key)
  if (!raw) return fallback

  const hex = colorToHex(raw)
  cache.set(key, hex)
  return hex
}

// The palette a canvas renderer needs, resolved in one go. Recomputes on a
// theme switch because it reads themeEpoch. The fallbacks are the light-theme
// values, so a test or a first paint without the stylesheet still gets a
// coherent set.
export const palette = computed(() => {
  void themeEpoch.value
  return {
    brand:          token('--brand', '#0a60ff'),
    brandHighlight: token('--brand-highlight', '#02c8ff'),

    good: token('--status-good', '#0ca30c'),
    warn: token('--status-warn', '#fab219'),
    bad:  token('--status-bad', '#d03b3b'),

    // Series slots, in the fixed categorical order.
    series: [
      token('--chart-1', '#2a78d6'),
      token('--chart-2', '#1baf7a'),
      token('--chart-3', '#eb6834'),
      token('--chart-4', '#02c8ff'),
      token('--chart-5', '#ff007f'),
      token('--chart-6', '#7c4dbd'),
      token('--chart-7', '#6b7280'),
    ],

    // Surfaces and type, for axes, labels and node chrome.
    ink:     token('--foreground', '#0b0b0b'),
    muted:   token('--muted-foreground', '#898781'),
    card:    token('--card', '#ffffff'),
    ground:  token('--background', '#ffffff'),
    border:  token('--border', '#e1e0d9'),
    primary: token('--primary', '#0a60ff'),
    grid:    token('--graph-grid', '#e1e0d9'),
    axis:    token('--graph-axis', '#c3c2b7'),
  }
})
