import { describe, expect, it } from 'vitest'
import { STATUS, chartTheme, utilizationBand, utilizationColor } from './chartTheme'

describe('utilizationBand', () => {
  it('has three bands with fixed thresholds', () => {
    expect(utilizationBand(0)).toBe('normal')
    expect(utilizationBand(69.9)).toBe('normal')
    expect(utilizationBand(70)).toBe('warning')
    expect(utilizationBand(89.9)).toBe('warning')
    expect(utilizationBand(90)).toBe('critical')
    expect(utilizationBand(150)).toBe('critical')
  })
})

describe('utilizationColor', () => {
  it('wears the first series slot until the warning band', () => {
    expect(utilizationColor(10)).toBe(chartTheme().series[0])
    expect(utilizationColor(75)).toBe(STATUS.warning)
    expect(utilizationColor(95)).toBe(STATUS.critical)
  })

  it('keeps status distinct from every series slot', () => {
    const series = new Set(chartTheme().series)
    for (const c of [STATUS.good, STATUS.warning, STATUS.critical]) {
      expect(series.has(c)).toBe(false)
    }
  })
})
