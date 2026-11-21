// Chart colors, resolved from the token layer in theme.css. The categorical
// order is a CVD-safety mechanism: assign slots in this fixed order by entity,
// never cycled or re-ranked.
//
// Nothing here holds a literal color. Change a --chart-* or --status-* token
// and every chart, graph and meter in the product follows.
import { palette } from './tokens'

export interface ChartTheme {
  series: string[]
  ink: string
  muted: string
  grid: string
  axis: string
  surface: string
}

// The chart palette for the active theme. The parameter is accepted for the
// call sites that pass a dark flag; the tokens already resolve to the active
// theme, so it is not read. A caller that rebuilds on a theme change should
// watch themeEpoch rather than the flag.
export function chartTheme(_dark?: boolean): ChartTheme {
  const p = palette.value
  return {
    series:  p.series,
    ink:     p.ink,
    muted:   p.muted,
    grid:    p.grid,
    axis:    p.axis,
    surface: 'transparent',
  }
}

// Status palette: reserved for state, and deliberately distinct from the
// categorical slots so a status color never impersonates a series. Fixed in
// both themes -- a permit/deny reading has to stay green/amber/red. Always
// shipped with a label so color never carries meaning alone.
export const STATUS = {
  get good() { return palette.value.good },
  get warning() { return palette.value.warn },
  get critical() { return palette.value.bad },
}

export type UtilizationBand = 'normal' | 'warning' | 'critical'

// How close a resource is to its ceiling. Three bands, not four: the warning
// and serious steps are only 13.6 apart in normal vision, which is below the
// separation floor for two colors that can appear in one chart together.
export function utilizationBand(percent: number): UtilizationBand {
  if (percent >= 90) return 'critical'
  if (percent >= 70) return 'warning'
  return 'normal'
}

export const UTILIZATION_BAND_LABELS: Record<UtilizationBand, string> = {
  normal:   'within capacity',
  warning:  'nearing capacity',
  critical: 'at capacity',
}

// The fill color for a utilization meter or bar. Normal usage wears the
// categorical accent; the two upper bands wear status colors.
export function utilizationColor(percent: number, dark?: boolean): string {
  const band = utilizationBand(percent)
  if (band === 'critical') return STATUS.critical
  if (band === 'warning') return STATUS.warning
  return chartTheme(dark).series[0]
}

// Shared axis/grid/tooltip scaffolding for a compact time-series chart.
export function baseTimeChartOption(t: ChartTheme) {
  return {
    backgroundColor: t.surface,
    grid: { left: 48, right: 12, top: 28, bottom: 24 },
    xAxis: {
      type: 'time' as const,
      axisLine: { lineStyle: { color: t.axis } },
      axisLabel: { color: t.muted, fontSize: 11 },
      splitLine: { show: false },
    },
    yAxis: {
      type: 'value' as const,
      scale: true,
      axisLabel: { color: t.muted, fontSize: 11 },
      splitLine: { lineStyle: { color: t.grid } },
    },
    tooltip: {
      trigger: 'axis' as const,
      className: 'themed-tooltip',
      textStyle: { color: 'inherit' as const, fontSize: 12 },
      extraCssText: 'border-radius: 6px;',
      axisPointer: { type: 'line' as const, lineStyle: { color: t.axis } },
    },
    animation: false,
  }
}
