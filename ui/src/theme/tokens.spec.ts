import { describe, expect, it } from 'vitest'
import { colorToHex, oklchToHex, palette, token } from './tokens'

describe('colorToHex', () => {
  it('passes a hex literal through', () => {
    expect(colorToHex(' #0A60FF ')).toBe('#0A60FF')
  })

  it('converts rgb() as some engines hand back', () => {
    expect(colorToHex('rgb(10, 96, 255)')).toBe('#0a60ff')
    expect(colorToHex('rgb(10 96 255 / 0.5)')).toBe('#0a60ff')
  })

  it('reads oklch as written in the stylesheet', () => {
    // #0A60FF, the value --brand carries.
    expect(colorToHex('oklch(0.553 0.247 262)')).toBe('#0a60ff')
  })

  it('reads the normalized form a computed style returns', () => {
    // Percent lightness and a bare-dot chroma resolve to the same color.
    expect(colorToHex('oklch(55.3% .247 262)')).toBe(colorToHex('oklch(0.553 0.247 262)'))
    // A percentage chroma is 0.4 at 100%.
    expect(colorToHex('oklch(55.3% 61.75% 262)')).toBe(colorToHex('oklch(0.553 0.247 262)'))
    expect(colorToHex('oklch(0.553 0.247 262deg)')).toBe('#0a60ff')
  })

  it('leaves anything else alone', () => {
    expect(colorToHex('transparent')).toBe('transparent')
    expect(colorToHex('oklch(x y z)')).toBe('oklch(x y z)')
  })
})

describe('oklchToHex', () => {
  it('clamps out-of-gamut channels rather than wrapping', () => {
    expect(oklchToHex(1, 0, 0)).toBe('#ffffff')
    expect(oklchToHex(0, 0, 0)).toBe('#000000')
    expect(oklchToHex(0.9, 0.4, 145)).toMatch(/^#[0-9a-f]{6}$/)
  })
})

describe('token', () => {
  it('falls back when the property is not defined', () => {
    expect(token('--no-such-token', '#123456')).toBe('#123456')
    expect(token('no-such-token', '#123456')).toBe('#123456')
  })

  it('resolves a property set on the root and follows a theme switch', () => {
    const root = document.documentElement
    root.style.setProperty('--spec-color', '#111111')
    if (!getComputedStyle(root).getPropertyValue('--spec-color')) {
      // The DOM shim does not compute custom properties; nothing to assert.
      return
    }
    expect(token('--spec-color')).toBe('#111111')
    root.style.setProperty('--spec-color', '#222222')
    // Same class list, so the cache still answers.
    expect(token('--spec-color')).toBe('#111111')
    // A class change on <html> is what a theme switch is; it invalidates
    // synchronously, before any observer has run.
    root.classList.add('dark')
    expect(token('--spec-color')).toBe('#222222')
    root.classList.remove('dark')
  })

  it('follows a brand change on the root as it does a theme switch', () => {
    const root = document.documentElement
    root.style.setProperty('--spec-brand', '#111111')
    if (!getComputedStyle(root).getPropertyValue('--spec-brand')) return
    expect(token('--spec-brand')).toBe('#111111')
    root.style.setProperty('--spec-brand', '#333333')
    expect(token('--spec-brand')).toBe('#111111')
    // A host-branded console sets data-brand when its brand arrives; that
    // invalidates synchronously too.
    root.dataset.brand = 'orange'
    expect(token('--spec-brand')).toBe('#333333')
    delete root.dataset.brand
  })
})

describe('palette', () => {
  it('has a coherent fallback set without a stylesheet', () => {
    const p = palette.value
    expect(p.series).toHaveLength(7)
    for (const c of [p.brand, p.good, p.warn, p.bad, p.ink, p.grid, ...p.series]) {
      expect(c).toMatch(/^#[0-9a-f]{6}$/i)
    }
  })
})
