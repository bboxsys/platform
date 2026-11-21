import { describe, it, expect } from 'vitest'
import {
  isZeroTime, fmtInt, fmtFeet, fmtMiles, fmtTempF, fmtLatLon, fmtPct,
  fmtDate, fmtDay, fmtBytes, shortKey, shortUserId,
} from './format'

describe('isZeroTime', () => {
  it('treats empty/null/undefined and the Go zero time as zero', () => {
    expect(isZeroTime(undefined)).toBe(true)
    expect(isZeroTime(null)).toBe(true)
    expect(isZeroTime('')).toBe(true)
    expect(isZeroTime('0001-01-01T00:00:00Z')).toBe(true)
  })

  it('treats a real timestamp as non-zero', () => {
    expect(isZeroTime('2026-08-30T12:00:00Z')).toBe(false)
  })
})

describe('numeric formatters', () => {
  it('fmtInt groups thousands', () => {
    expect(fmtInt(1234567)).toBe('1,234,567')
  })

  it('fmtFeet converts meters to feet', () => {
    expect(fmtFeet(100)).toBe('328.1 ft')
  })

  it('fmtMiles fixes two decimals', () => {
    expect(fmtMiles(3.14159)).toBe('3.14 mi')
  })

  it('fmtTempF converts celsius to fahrenheit', () => {
    expect(fmtTempF(0)).toBe('32.0 F')
    expect(fmtTempF(100)).toBe('212.0 F')
  })

  it('fmtLatLon fixes five decimals', () => {
    expect(fmtLatLon(30.123456)).toBe('30.12346')
  })

  it('fmtPct fixes one decimal', () => {
    expect(fmtPct(42.567)).toBe('42.6%')
  })
})

describe('fmtDate / fmtDay', () => {
  it('returns empty for missing or nonsense input', () => {
    expect(fmtDate('')).toBe('')
    expect(fmtDate(null)).toBe('')
    expect(fmtDate(undefined)).toBe('')
    expect(fmtDate('not-a-date')).toBe('')
    expect(fmtDate('0001-01-01T00:00:00Z')).toBe('') // pre-1900 guard
  })

  it('formats a valid ISO date', () => {
    // Assert it produces a non-empty string containing the year rather than
    // pinning an exact locale/timezone-sensitive rendering.
    expect(fmtDate('2026-08-30T12:00:00Z')).toContain('2026')
    expect(fmtDay('2026-08-30T12:00:00Z')).toContain('2026')
  })
})

describe('fmtBytes', () => {
  it('rejects invalid input', () => {
    expect(fmtBytes(-1)).toBe('')
    expect(fmtBytes(NaN)).toBe('')
    expect(fmtBytes(Infinity)).toBe('')
  })

  it('formats across units', () => {
    expect(fmtBytes(0)).toBe('0 B')
    expect(fmtBytes(512)).toBe('512 B')
    expect(fmtBytes(1024)).toBe('1.0 KB')
    expect(fmtBytes(1536)).toBe('1.5 KB')
    expect(fmtBytes(1024 * 1024)).toBe('1.0 MB')
    expect(fmtBytes(1024 * 1024 * 1024)).toBe('1.0 GB')
  })
})

describe('shortKey', () => {
  it('leaves short keys untouched', () => {
    expect(shortKey('abc')).toBe('abc')
  })

  it('elides the middle of long keys', () => {
    const key = 'abcdefghijklmnopqrstuvwxyz'
    expect(shortKey(key)).toBe('abcdefgh...wxyz')
  })
})

describe('shortUserId', () => {
  it('returns the email part after the account prefix', () => {
    expect(shortUserId('acct123:user@example.com')).toBe('user@example.com')
  })

  it('returns the id unchanged when there is no colon', () => {
    expect(shortUserId('plainid')).toBe('plainid')
  })
})
