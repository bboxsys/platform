import { describe, it, expect } from 'vitest'
import { haversineMiles, pathMiles } from './geo'

describe('haversineMiles', () => {
  it('is zero for identical points', () => {
    expect(haversineMiles(30, -96, 30, -96)).toBe(0)
  })

  it('is symmetric', () => {
    const a = haversineMiles(30, -96, 31, -95)
    const b = haversineMiles(31, -95, 30, -96)
    expect(a).toBeCloseTo(b, 10)
  })

  it('computes a known distance (roughly one degree of latitude ~= 69 mi)', () => {
    const d = haversineMiles(30, -96, 31, -96)
    expect(d).toBeGreaterThan(68)
    expect(d).toBeLessThan(70)
  })

  it('matches a longer reference leg (Austin to Dallas ~= 182 mi)', () => {
    const d = haversineMiles(30.2672, -97.7431, 32.7767, -96.797)
    expect(d).toBeGreaterThan(175)
    expect(d).toBeLessThan(190)
  })
})

describe('pathMiles', () => {
  it('returns 0 for an empty path or a single point', () => {
    expect(pathMiles([])).toBe(0)
    expect(pathMiles([{ lat: 30, lon: -96 }])).toBe(0)
  })

  it('sums the leg distances', () => {
    const pts = [
      { lat: 30, lon: -96 },
      { lat: 31, lon: -96 },
      { lat: 32, lon: -96 },
    ]
    const expected = haversineMiles(30, -96, 31, -96) + haversineMiles(31, -96, 32, -96)
    expect(pathMiles(pts)).toBeCloseTo(expected, 10)
  })

  it('skips points missing lat or lon and bridges across them', () => {
    const withGap = [
      { lat: 30, lon: -96 },
      { lat: 31 }, // no lon -> skipped
      { lon: -96 }, // no lat -> skipped
      { lat: 32, lon: -96 },
    ]
    // The two valid points bridge directly.
    expect(pathMiles(withGap)).toBeCloseTo(haversineMiles(30, -96, 32, -96), 10)
  })
})
