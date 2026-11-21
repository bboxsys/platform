<script lang="ts">
// One chart from an echarts fenced block: the model's option, as JSON,
// drawn by Apache ECharts under the kit's chart theme. JSON.parse is the
// whole trust boundary: an option is data, never code, so no formatter
// functions or callbacks reach ECharts. Colors, fonts, and mark styling
// come from the theme (the prompt tells the model to leave them out), so a
// chart from the model looks like every other chart in the app and
// follows the light/dark switch. Exported for the unit tests.
import { chartTheme, type ChartTheme } from '../../theme/chartTheme'

export type Option = Record<string, unknown>

export function parseOption(source: string): { option: Option } | { error: string } {
  let parsed: unknown
  try {
    parsed = JSON.parse(source)
  } catch (e) {
    return { error: e instanceof Error ? e.message : 'invalid JSON' }
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return { error: 'the option must be a JSON object' }
  const option = parsed as Option
  if (!Array.isArray(option.series) && !(option.series && typeof option.series === 'object')) return { error: 'the option has no series' }
  return { option }
}

// The ECharts theme built from the kit palette: categorical slots in their
// fixed order, text in ink tokens, recessive axes and grid, thin marks.
// fontFamily is the resolved page font: ECharts measures text with it, so
// 'inherit' would measure with a fallback font and misplace labels.
export function echartsTheme(t: ChartTheme, fontFamily = 'sans-serif'): Record<string, unknown> {
  const axis = {
    axisLine: { lineStyle: { color: t.axis } },
    axisTick: { show: false },
    axisLabel: { color: t.muted, fontSize: 11 },
    nameTextStyle: { color: t.muted },
    splitLine: { lineStyle: { color: t.grid } },
  }
  return {
    color: t.series,
    backgroundColor: 'transparent',
    textStyle: { color: t.ink, fontFamily },
    title: { textStyle: { color: t.ink, fontSize: 13, fontWeight: 600 }, subtextStyle: { color: t.muted, fontSize: 11 } },
    legend: { textStyle: { color: t.muted, fontSize: 11 }, itemWidth: 10, itemHeight: 10, icon: 'roundRect' },
    categoryAxis: { ...axis, splitLine: { show: false } },
    valueAxis: axis,
    timeAxis: { ...axis, splitLine: { show: false } },
    logAxis: axis,
    bar: { barMaxWidth: 32, itemStyle: { borderRadius: [4, 4, 0, 0] } },
    line: { lineStyle: { width: 2 }, symbolSize: 8, smooth: false },
    pie: { itemStyle: { borderColor: 'transparent', borderWidth: 2 }, label: { color: t.ink } },
    scatter: { symbolSize: 8 },
  }
}

const isRecord = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v)
const asList = (v: unknown): Record<string, unknown>[] => (Array.isArray(v) ? v : v ? [v] : []).filter(isRecord)

// Layout defaults the theme cannot carry: a tooltip on every chart, a grid
// that fits its labels, the legend only when there is more than one
// series, and bar ends rounded on the side away from the baseline.
export function applyDefaults(option: Option): Option {
  const out: Option = { ...option }
  const series = asList(out.series)
  const horizontal = asList(out.yAxis).some(a => a.type === 'category') && !asList(out.xAxis).some(a => a.type === 'category')
  out.series = series.map(s => {
    if (s.type !== 'bar') return s
    const itemStyle = isRecord(s.itemStyle) ? s.itemStyle : {}
    return { ...s, itemStyle: { borderRadius: horizontal ? [0, 4, 4, 0] : [4, 4, 0, 0], ...itemStyle } }
  })
  // Category labels on a ranking are long (log messages, service names):
  // show every one, truncated to fit, instead of letting ECharts drop the
  // ones that would overlap. Vertical bars with many categories lean the
  // labels instead.
  const labelAxis = horizontal ? 'yAxis' : 'xAxis'
  const categories = asList(out[labelAxis]).filter(a => a.type === 'category')
  if (categories.length > 0) {
    out[labelAxis] = asList(out[labelAxis]).map(a => {
      if (a.type !== 'category') return a
      const axisLabel = isRecord(a.axisLabel) ? a.axisLabel : {}
      const count = Array.isArray(a.data) ? a.data.length : 0
      const defaults = horizontal
        ? { interval: 0, width: 220, overflow: 'truncate' }
        : count > 6 ? { interval: 0, rotate: 30, width: 110, overflow: 'truncate' } : {}
      return { ...a, axisLabel: { ...defaults, ...axisLabel } }
    })
  }
  const cartesian = series.some(s => s.type === 'bar' || s.type === 'line' || s.type === 'scatter')
  // The tooltip is always the app's: whatever the model sent keeps its
  // trigger and formatter, but the popover surface, text color, and
  // border come from the theme class (its rules are !important).
  const tooltip = isRecord(out.tooltip) ? out.tooltip : {}
  out.tooltip = {
    trigger: cartesian ? 'axis' : 'item',
    ...tooltip,
    className: 'themed-tooltip',
    textStyle: { ...(isRecord(tooltip.textStyle) ? tooltip.textStyle : {}), color: 'inherit', fontSize: 12 },
    extraCssText: 'border-radius: 6px;',
  }
  if (cartesian && out.grid === undefined) {
    out.grid = { left: 8, right: 16, top: out.title ? 44 : 24, bottom: series.length > 1 ? 36 : 12, containLabel: true }
  }
  if (out.legend === undefined && series.length > 1) {
    out.legend = { bottom: 0 }
  }
  if (isRecord(out.title) && out.title.left === undefined) out.title = { ...out.title, left: 0 }
  out.animation = false
  return out
}
</script>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import { useResizeObserver } from '@vueuse/core'
import { DownloadIcon } from '@lucide/vue'
import type { ECharts } from 'echarts/core'
import { token } from '../../theme/tokens'
import { useTheme } from '../../theme/useTheme'

const props = defineProps<{ source: string }>()

const parsed = computed(() => parseOption(props.source))
const host = ref<HTMLElement | null>(null)
const chart = shallowRef<ECharts | null>(null)
const failed = ref('')
const { themeEpoch, isDark } = useTheme()

// Loaded on first use so a page does not carry ECharts until a chart
// appears; echarts.ts registers the series and components.
let modulePromise: Promise<typeof import('./echarts')> | null = null
function loadECharts() {
  if (!modulePromise) modulePromise = import('./echarts')
  return modulePromise
}

let themeSeq = 0
async function draw() {
  const el = host.value
  const p = parsed.value
  if (!el || 'error' in p) return
  try {
    const core = await loadECharts()
    if (host.value !== el) return
    chart.value?.dispose()
    // A theme is registered by name; one per theme epoch so a switch to
    // dark re-reads the tokens instead of reusing the light palette.
    const name = `bbox-${themeEpoch.value}-${isDark.value ? 'dark' : 'light'}-${themeSeq++}`
    core.registerTheme(name, echartsTheme(chartTheme(isDark.value), getComputedStyle(el).fontFamily || 'sans-serif'))
    const c = core.init(el, name, { renderer: 'svg' })
    c.setOption(applyDefaults(p.option))
    chart.value = c
    failed.value = ''
  } catch (e) {
    failed.value = e instanceof Error ? e.message : 'the chart could not be drawn'
  }
}

// The chart as the SVG ECharts drew it, on the page background so the
// file reads outside the app. Named after the chart's title.
function downloadSVG() {
  const c = chart.value
  if (!c) return
  const title = 'error' in parsed.value ? undefined : parsed.value.option.title
  const text = title && typeof title === 'object' && !Array.isArray(title) ? (title as Record<string, unknown>).text : undefined
  const name = (typeof text === 'string' && text.trim() ? text.trim() : 'chart').replace(/[^\w.-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 80) || 'chart'
  // token() resolves the oklch value to hex, which every SVG viewer reads.
  const background = token('--background', isDark.value ? '#000000' : '#ffffff')
  const url = c.getDataURL({ type: 'svg', backgroundColor: background })
  const a = document.createElement('a')
  a.href = url
  a.download = `${name}.svg`
  a.click()
}

watch([host, parsed, themeEpoch, isDark], () => { void draw() }, { immediate: true, flush: 'post' })
useResizeObserver(host, () => chart.value?.resize())
onBeforeUnmount(() => chart.value?.dispose())
</script>

<template>
  <div class="my-2" data-slot="assistant-chart">
    <div v-if="'error' in parsed || failed" class="rounded-md border border-dashed px-3 py-2 text-xs">
      <div class="mb-1 text-destructive">Chart not drawn: {{ 'error' in parsed ? parsed.error : failed }}</div>
      <pre class="max-h-48 overflow-auto whitespace-pre-wrap font-mono text-muted-foreground">{{ source }}</pre>
    </div>
    <div v-else class="group relative">
      <div ref="host" class="h-72 w-full" />
      <button
        v-if="chart"
        type="button"
        class="absolute right-1 top-1 rounded-md border bg-card p-1.5 text-muted-foreground opacity-0 transition-opacity hover:text-foreground focus-visible:opacity-100 group-hover:opacity-100"
        title="Download SVG"
        aria-label="Download SVG"
        @click="downloadSVG"
      >
        <DownloadIcon class="size-3.5" />
      </button>
    </div>
  </div>
</template>
