import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { colorToHex } from '../theme/tokens'

// A brand preset is the complete set of brand tokens for both themes, in a
// form the canvas bridge can resolve. A preset missing one token would leave
// that token at the default brand, which shows up as a wrong-colored button in
// one theme only.
const BRAND_TOKENS = ['--brand', '--brand-foreground', '--brand-highlight', '--brand-highlight-ink']
// Read from disk: vitest hands back an empty module for a stylesheet import,
// raw query or not.
const dir = join(__dirname, 'brands')
const PRESETS: Record<string, string> = Object.fromEntries(
  readdirSync(dir).filter((f) => f.endsWith('.css')).map((f) => [f, readFileSync(join(dir, f), 'utf8')]),
)

function block(css: string, selector: string): string {
  const m = css.match(new RegExp(String.raw`${selector.replace('.', String.raw`\.`)}\s*\{([^}]*)\}`))
  expect(m, `${selector} block`).not.toBeNull()
  return m![1]
}

describe('brand presets', () => {
  it('ships the two presets', () => {
    expect(Object.keys(PRESETS).sort()).toEqual(['blue.css', 'orange.css'])
  })

  for (const [name, css] of Object.entries(PRESETS)) {
    it(`${name} sets every brand token in both themes as a resolvable color`, () => {
      expect(css.length).toBeGreaterThan(0)
      expect(block(css, ':root')).toMatch(/--brand-hue:\s*\d+/)
      for (const selector of [':root', '.dark']) {
        const body = block(css, selector)
        for (const token of BRAND_TOKENS) {
          const m = body.match(new RegExp(String.raw`${token}:\s*([^;]+);`))
          expect(m, `${name} ${selector} ${token}`).not.toBeNull()
          expect(colorToHex(m![1])).toMatch(/^#[0-9a-f]{6}$/)
        }
      }
    })
  }
})
