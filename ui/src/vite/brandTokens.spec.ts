import { describe, expect, it } from 'vitest'
// Plain ESM JavaScript; its types live in devProxy.d.mts.
import { BRAND_TOKENS_FILE, brandTokens, declarations, extractBrandTokens, starlightBrandCSS } from './brandTokens.mjs'

const BRAND = `@import url('https://fonts.example/css2?family=Geist');
/* The brand: green and gold. */
@font-face { font-family: 'Face'; src: url('./assets/face.woff2') format('woff2'); }

:root {
  --brand-hue: 155;
  --brand: oklch(0.52 0.14 155); /* deep green */
  --brand-foreground: oklch(0.985 0 0);
  --logo: url('./assets/logo.svg');
  --chart-1: oklch(0.55 0.14 155);
}

.dark {
  --brand: oklch(0.7 0.15 155);
  --chart-1: oklch(0.7 0.15 155);
}

@theme inline {
  --font-sans: 'Geist', sans-serif;
}

.card { color: var(--brand); }
`

describe('declarations', () => {
  it('keeps custom properties in order and drops everything else', () => {
    expect(declarations('color: red; --a: 1; --b: var(--a); --c: url(x.png);')).toEqual([['--a', '1'], ['--b', 'var(--a)']])
  })
})

describe('extractBrandTokens', () => {
  it('reduces a brand stylesheet to its :root and .dark tokens', () => {
    expect(extractBrandTokens(BRAND)).toBe(
      ':root {\n  --brand-hue: 155;\n  --brand: oklch(0.52 0.14 155);\n  --brand-foreground: oklch(0.985 0 0);\n  --chart-1: oklch(0.55 0.14 155);\n}\n' +
      '.dark {\n  --brand: oklch(0.7 0.15 155);\n  --chart-1: oklch(0.7 0.15 155);\n}\n',
    )
  })

  it('leaves out a .dark block the stylesheet does not have', () => {
    expect(extractBrandTokens(':root { --brand-hue: 10; }')).toBe(':root {\n  --brand-hue: 10;\n}\n')
  })

  it('merges repeated blocks', () => {
    expect(extractBrandTokens(':root { --brand-hue: 10; }\n:root { --x: 1; }')).toBe(':root {\n  --brand-hue: 10;\n  --x: 1;\n}\n')
  })

  it('rejects a stylesheet with no :root block', () => {
    expect(() => extractBrandTokens('.dark { --brand: red; }')).toThrow(/no :root/)
  })

  it('rejects a stylesheet that sets no brand', () => {
    expect(() => extractBrandTokens(':root { --chart-1: red; }')).toThrow(/--brand/)
  })
})

describe('starlightBrandCSS', () => {
  it('maps :root and .dark onto the Starlight themes', () => {
    expect(starlightBrandCSS(BRAND)).toBe(
      ':root {\n  --brand-hue: 155;\n  --brand: oklch(0.7 0.15 155);\n  --brand-foreground: oklch(0.985 0 0);\n  --chart-1: oklch(0.7 0.15 155);\n}\n' +
      ":root[data-theme='light'] {\n  --brand-hue: 155;\n  --brand: oklch(0.52 0.14 155);\n  --brand-foreground: oklch(0.985 0 0);\n  --chart-1: oklch(0.55 0.14 155);\n}\n",
    )
  })
})

describe('brandTokens', () => {
  it('is a build-only plugin that emits brand-tokens.css', () => {
    const plugin = brandTokens() as unknown as {
      name: string
      apply: string
      configResolved: (config: { root: string }) => void
      generateBundle: (this: { emitFile: (f: unknown) => void }) => void
    }
    expect(plugin.name).toBe('@bboxsys/ui:brand-tokens')
    expect(plugin.apply).toBe('build')
    expect(BRAND_TOKENS_FILE).toBe('brand-tokens.css')
    // A missing source fails the build with the path in the message rather
    // than emitting an empty brand.
    plugin.configResolved({ root: '/nonexistent/app' })
    const emitted: unknown[] = []
    expect(() => plugin.generateBundle.call({ emitFile: (f: unknown) => emitted.push(f) })).toThrow(/\/nonexistent\/app\/src\/brand\.css/)
    expect(emitted).toEqual([])
  })
})
