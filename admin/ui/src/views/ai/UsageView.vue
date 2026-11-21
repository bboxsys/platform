<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { watchDebounced } from '@vueuse/core'
import { ActivityIcon, AlertTriangleIcon, ArrowDownToLineIcon, ArrowUpFromLineIcon, DatabaseIcon, RefreshCwIcon, XIcon } from '@lucide/vue'
import { aiApi } from '@/api/admin'
import { useAsync, usePolling, errorMessage } from '@bboxsys/ui'
import type { AIInstanceSummary, AIUsageGroup, AIUsageRecord, AIUsageTotals } from '@bboxsys/ui'
import { fmtDateTime, fmtInt, fmtRelative, shortKey } from '@bboxsys/ui'
import { DataTable, MetricTile, PageHeader, PageTabs, StatusBadge, type Column } from '@bboxsys/ui'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

const route = useRoute()
const router = useRouter()

const PAGE = 100
const ALL = '__all__'
const WINDOW_DAYS = 30
const EMPTY = 'No AI instance has reported yet. Usage appears here once an assistant runs.'

// The tiles and the Models tab cover the last 30 days, the instances'
// own retention window.
function since(): string {
  return new Date(Date.now() - WINDOW_DAYS * 24 * 3600 * 1000).toISOString()
}

const instances = useAsync(() => aiApi.usage.instances(), [] as AIInstanceSummary[])
const byInstance = useAsync(() => aiApi.usage.summary(since(), 'instance'), [] as AIUsageGroup[])
const byModel = useAsync(() => aiApi.usage.summary(since(), 'model'), [] as AIUsageGroup[])

// ---- Tabs, kept in ?tab= --------------------------------------------------

type Tab = 'instances' | 'runs' | 'models'
const TAB_ORDER: Tab[] = ['instances', 'runs', 'models']

const tab = computed<Tab>({
  get: () => (TAB_ORDER.includes(route.query.tab as Tab) ? route.query.tab as Tab : 'instances'),
  set: v => { router.replace({ query: { ...route.query, tab: v === 'instances' ? undefined : v } }) },
})
const tabItems = computed(() => [
  { value: 'instances', label: 'Instances', count: instances.data.value.length },
  { value: 'runs', label: 'Runs' },
  { value: 'models', label: 'Models', count: byModel.data.value.length },
])

// ---- Tiles ----------------------------------------------------------------

const totals = computed<AIUsageTotals>(() => {
  const t: AIUsageTotals = {
    runs: 0, turns: 0, tool_calls: 0, errors: 0,
    usage: { input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 },
  }
  for (const g of byInstance.data.value) {
    t.runs += g.totals.runs
    t.turns += g.totals.turns
    t.tool_calls += g.totals.tool_calls
    t.errors += g.totals.errors
    t.usage.input_tokens += g.totals.usage.input_tokens
    t.usage.output_tokens += g.totals.usage.output_tokens
    t.usage.cache_read_tokens += g.totals.usage.cache_read_tokens
    t.usage.cache_write_tokens += g.totals.usage.cache_write_tokens
  }
  return t
})

// ---- Instances ------------------------------------------------------------

const instanceColumns: Column<AIInstanceSummary>[] = [
  { key: 'instance', label: 'Instance', sortable: true, value: i => `${i.instance} ${i.service ?? ''}` },
  { key: 'host', label: 'Host', sortable: true },
  { key: 'model', label: 'Model', sortable: true, value: i => i.model ?? '' },
  { key: 'online', label: 'Status', sortable: true, value: i => (i.online ? 'online' : 'offline') },
  { key: 'runs', label: 'Runs', sortable: true, class: 'text-right tabular-nums', value: i => i.totals.runs },
  { key: 'input', label: 'Input', sortable: true, class: 'text-right tabular-nums', value: i => i.totals.usage.input_tokens },
  { key: 'output', label: 'Output', sortable: true, class: 'text-right tabular-nums', value: i => i.totals.usage.output_tokens },
  { key: 'cache_read', label: 'Cache read', sortable: true, class: 'text-right tabular-nums', value: i => i.totals.usage.cache_read_tokens },
  { key: 'errors', label: 'Errors', sortable: true, class: 'text-right tabular-nums', value: i => i.totals.errors },
  { key: 'last_seen', label: 'Last seen', sortable: true, value: i => i.last_seen },
  { key: 'started_at', label: 'Started', sortable: true, value: i => i.started_at },
]

function showRuns(i: AIInstanceSummary) {
  router.replace({ query: { ...route.query, tab: 'runs', instance: i.instance, host: i.host } })
}

// ---- Runs (server-paged) --------------------------------------------------

// The instance filter lives in the query beside the tab, so a reload or a
// link keeps it; the model filter is typed and stays local.
const runInstance = computed(() => (typeof route.query.instance === 'string' ? route.query.instance : ''))
const runHost = computed(() => (typeof route.query.host === 'string' ? route.query.host : ''))
const modelInput = ref('')
const runModel = ref('')
watchDebounced(modelInput, v => { runModel.value = v.trim() }, { debounce: 300 })

const instanceSelect = computed<string>({
  get: () => (runInstance.value ? `${runInstance.value}@${runHost.value}` : ALL),
  set: v => {
    const i = instances.data.value.find(x => x.id === v)
    router.replace({ query: { ...route.query, instance: i?.instance, host: i?.host } })
  },
})

function clearInstance() {
  router.replace({ query: { ...route.query, instance: undefined, host: undefined } })
}

const runQuery = computed(() => ({
  instance: runInstance.value || undefined,
  host: runHost.value || undefined,
  model: runModel.value || undefined,
}))

// Pages are appended as the reader nears the end and keyed by id, so an
// offset that shifted because new runs arrived in between never shows a run
// twice. Each reload bumps the generation; a page for an older generation
// (the filters changed while it was in flight) is dropped.
const runs = ref<AIUsageRecord[]>([])
const runTotal = ref(0)
const runsLoading = ref(false)
const runsError = ref('')
const runsLoaded = ref(false)
const hasMore = computed(() => runs.value.length < runTotal.value)
let generation = 0

function fetchRuns(offset: number) {
  return aiApi.usage.runs({ ...runQuery.value, limit: PAGE, offset })
}

async function reloadRuns() {
  const gen = ++generation
  runsLoading.value = true
  runsError.value = ''
  try {
    const page = await fetchRuns(0)
    if (gen !== generation) return
    runs.value = page.records ?? []
    runTotal.value = page.total
    runsLoaded.value = true
  } catch (e) {
    if (gen === generation) runsError.value = errorMessage(e)
  } finally {
    if (gen === generation) runsLoading.value = false
  }
}

async function loadMoreRuns() {
  if (runsLoading.value || !hasMore.value) return
  const gen = generation
  runsLoading.value = true
  try {
    const page = await fetchRuns(runs.value.length)
    if (gen !== generation) return
    const seen = new Set(runs.value.map(r => r.id))
    runs.value = [...runs.value, ...(page.records ?? []).filter(r => !seen.has(r.id))]
    runTotal.value = page.total
  } catch (e) {
    if (gen === generation) runsError.value = errorMessage(e)
  } finally {
    if (gen === generation) runsLoading.value = false
  }
}

// Runs load when their tab is first shown and again whenever a filter changes.
watch(runQuery, () => { if (tab.value === 'runs' || runsLoaded.value) reloadRuns() })
watch(tab, t => { if (t === 'runs' && !runsLoaded.value) reloadRuns() })

const runColumns: Column<AIUsageRecord>[] = [
  { key: 'at', label: 'Time', class: 'w-44 text-muted-foreground tabular-nums' },
  { key: 'instance', label: 'Instance', value: r => `${r.instance}@${r.host}` },
  { key: 'account', label: 'Account', value: r => r.source_account || r.account_id },
  { key: 'model', label: 'Model' },
  { key: 'turns', label: 'Turns', class: 'text-right tabular-nums' },
  { key: 'tool_calls', label: 'Tools', class: 'text-right tabular-nums' },
  { key: 'input', label: 'Input', class: 'text-right tabular-nums', value: r => r.usage.input_tokens },
  { key: 'output', label: 'Output', class: 'text-right tabular-nums', value: r => r.usage.output_tokens },
  { key: 'cache_read', label: 'Cache read', class: 'text-right tabular-nums', value: r => r.usage.cache_read_tokens },
  { key: 'duration', label: 'Duration', class: 'text-right tabular-nums' },
  { key: 'result', label: 'Result' },
]

// Duration is a Go time.Duration: nanoseconds.
function fmtDuration(ns: number): string {
  const ms = ns / 1e6
  if (ms < 1000) return `${Math.round(ms)} ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`
  return `${Math.floor(ms / 60_000)} m ${Math.round((ms % 60_000) / 1000)} s`
}

const runsEmpty = computed(() =>
  runInstance.value || runModel.value ? 'No runs match.' : instances.data.value.length ? 'No runs in the last 30 days.' : EMPTY)

// ---- Models ---------------------------------------------------------------

const modelColumns: Column<AIUsageGroup>[] = [
  { key: 'key', label: 'Model', sortable: true, value: g => g.key || '(none)' },
  { key: 'runs', label: 'Runs', sortable: true, class: 'text-right tabular-nums', value: g => g.totals.runs },
  { key: 'turns', label: 'Turns', sortable: true, class: 'text-right tabular-nums', value: g => g.totals.turns },
  { key: 'tools', label: 'Tools', sortable: true, class: 'text-right tabular-nums', value: g => g.totals.tool_calls },
  { key: 'input', label: 'Input', sortable: true, class: 'text-right tabular-nums', value: g => g.totals.usage.input_tokens },
  { key: 'output', label: 'Output', sortable: true, class: 'text-right tabular-nums', value: g => g.totals.usage.output_tokens },
  { key: 'cache_read', label: 'Cache read', sortable: true, class: 'text-right tabular-nums', value: g => g.totals.usage.cache_read_tokens },
  { key: 'cache_write', label: 'Cache write', sortable: true, class: 'text-right tabular-nums', value: g => g.totals.usage.cache_write_tokens },
  { key: 'errors', label: 'Errors', sortable: true, class: 'text-right tabular-nums', value: g => g.totals.errors },
]

// ---- Loading --------------------------------------------------------------

function refresh() {
  instances.run()
  byInstance.run()
  byModel.run()
  if (tab.value === 'runs' || runsLoaded.value) reloadRuns()
}

onMounted(() => {
  instances.run()
  byInstance.run()
  byModel.run()
  if (tab.value === 'runs') reloadRuns()
})

// Instances go online and offline by heartbeat; keep that current while the
// page is visible.
usePolling(() => { instances.run() }, 30_000, false)
</script>

<template>
  <PageHeader title="AI usage" description="Runs, tokens, and errors reported by every AI instance on the platform.">
    <span class="text-xs text-muted-foreground">Last {{ WINDOW_DAYS }} days</span>
    <Button variant="outline" size="sm" @click="refresh"><RefreshCwIcon /> Refresh</Button>
  </PageHeader>

  <div class="flex flex-col gap-4 px-6 pb-6">
    <div class="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
      <MetricTile label="Runs" :value="fmtInt(totals.runs)" :icon="ActivityIcon" :caption="`${fmtInt(totals.turns)} turns, ${fmtInt(totals.tool_calls)} tool calls`" />
      <MetricTile label="Input tokens" :value="fmtInt(totals.usage.input_tokens)" :icon="ArrowUpFromLineIcon" />
      <MetricTile label="Output tokens" :value="fmtInt(totals.usage.output_tokens)" :icon="ArrowDownToLineIcon" />
      <MetricTile label="Cache read tokens" :value="fmtInt(totals.usage.cache_read_tokens)" :icon="DatabaseIcon" rail="highlight" />
      <MetricTile label="Errors" :value="fmtInt(totals.errors)" :icon="AlertTriangleIcon" :rail="totals.errors ? 'bad' : 'default'" />
    </div>
    <p v-if="byInstance.error.value" class="text-sm text-destructive">{{ byInstance.error.value }}</p>

    <PageTabs
      :tabs="tabItems" :model-value="tab" label="Usage views"
      @update:model-value="v => (tab = v as Tab)"
    />

    <DataTable
      v-if="tab === 'instances'"
      :columns="instanceColumns"
      :rows="instances.data.value"
      :row-key="i => i.id"
      :loading="instances.loading.value && !instances.data.value.length"
      :error="instances.error.value"
      :empty-text="EMPTY"
      search-placeholder="Filter instances..."
      clickable
      max-height="calc(100dvh - 22rem)"
      @row-click="showRuns"
    >
      <template #cell-instance="{ row }">
        <div class="flex flex-col">
          <span class="font-medium">{{ row.instance }}</span>
          <span v-if="row.service" class="text-xs text-muted-foreground">{{ row.service }}</span>
        </div>
      </template>
      <template #cell-host="{ row }"><span class="text-muted-foreground">{{ row.host }}</span></template>
      <template #cell-model="{ row }">
        <span :title="row.provider">{{ row.model ?? '' }}</span>
      </template>
      <template #cell-online="{ row }">
        <StatusBadge :status="row.online ? 'active' : 'offline'" />
      </template>
      <template #cell-runs="{ row }">{{ fmtInt(row.totals.runs) }}</template>
      <template #cell-input="{ row }">{{ fmtInt(row.totals.usage.input_tokens) }}</template>
      <template #cell-output="{ row }">{{ fmtInt(row.totals.usage.output_tokens) }}</template>
      <template #cell-cache_read="{ row }">{{ fmtInt(row.totals.usage.cache_read_tokens) }}</template>
      <template #cell-errors="{ row }">
        <span :class="row.totals.errors ? 'text-destructive' : ''">{{ fmtInt(row.totals.errors) }}</span>
      </template>
      <template #cell-last_seen="{ row }">
        <span class="text-muted-foreground" :title="fmtDateTime(row.last_seen)">{{ fmtRelative(row.last_seen) }}</span>
      </template>
      <template #cell-started_at="{ row }">
        <span class="text-muted-foreground">{{ fmtDateTime(row.started_at) }}</span>
      </template>
    </DataTable>

    <template v-else-if="tab === 'runs'">
      <div class="flex flex-wrap items-center gap-2">
        <Select v-model="instanceSelect">
          <SelectTrigger class="w-64"><SelectValue placeholder="Instance" /></SelectTrigger>
          <SelectContent>
            <SelectItem :value="ALL">All instances</SelectItem>
            <SelectItem v-for="i in instances.data.value" :key="i.id" :value="i.id">{{ i.id }}</SelectItem>
          </SelectContent>
        </Select>
        <Input v-model="modelInput" placeholder="Model" class="w-52" />
        <Badge v-if="runInstance" variant="secondary" class="gap-1 pr-1">
          {{ runInstance }}@{{ runHost }}
          <button type="button" class="rounded-sm p-0.5 hover:bg-muted" title="Show every instance" @click="clearInstance">
            <XIcon class="size-3" />
          </button>
        </Badge>
        <span class="ml-auto text-xs tabular-nums text-muted-foreground">
          {{ fmtInt(runs.length) }} of {{ fmtInt(runTotal) }} runs loaded
        </span>
      </div>

      <DataTable
        class="text-xs"
        :columns="runColumns"
        :rows="runs"
        :row-key="r => r.id"
        :loading="runsLoading"
        :error="runsError"
        :has-more="hasMore"
        :empty-text="runsEmpty"
        :searchable="false"
        max-height="calc(100dvh - 25rem)"
        @load-more="loadMoreRuns"
      >
        <template #cell-at="{ row }">{{ fmtDateTime(row.at) }}</template>
        <template #cell-instance="{ row }">
          <span>{{ row.instance }}</span><span class="text-muted-foreground">@{{ row.host }}</span>
        </template>
        <template #cell-account="{ row }">
          <span class="font-mono text-muted-foreground" :title="row.source_account || row.account_id">
            {{ shortKey(row.source_account || row.account_id) }}
          </span>
        </template>
        <template #cell-turns="{ row }">{{ fmtInt(row.turns) }}</template>
        <template #cell-tool_calls="{ row }">{{ fmtInt(row.tool_calls) }}</template>
        <template #cell-input="{ row }">{{ fmtInt(row.usage.input_tokens) }}</template>
        <template #cell-output="{ row }">{{ fmtInt(row.usage.output_tokens) }}</template>
        <template #cell-cache_read="{ row }">{{ fmtInt(row.usage.cache_read_tokens) }}</template>
        <template #cell-duration="{ row }">{{ fmtDuration(row.duration) }}</template>
        <template #cell-result="{ row }">
          <Badge v-if="row.error" variant="destructive" class="max-w-48 truncate" :title="row.error">error</Badge>
          <span v-else class="text-muted-foreground">{{ row.stop_reason ?? '' }}</span>
        </template>
      </DataTable>
    </template>

    <DataTable
      v-else
      :columns="modelColumns"
      :rows="byModel.data.value"
      :row-key="g => g.key"
      :loading="byModel.loading.value && !byModel.data.value.length"
      :error="byModel.error.value"
      :empty-text="EMPTY"
      search-placeholder="Filter models..."
      max-height="calc(100dvh - 22rem)"
    >
      <template #cell-runs="{ row }">{{ fmtInt(row.totals.runs) }}</template>
      <template #cell-turns="{ row }">{{ fmtInt(row.totals.turns) }}</template>
      <template #cell-tools="{ row }">{{ fmtInt(row.totals.tool_calls) }}</template>
      <template #cell-input="{ row }">{{ fmtInt(row.totals.usage.input_tokens) }}</template>
      <template #cell-output="{ row }">{{ fmtInt(row.totals.usage.output_tokens) }}</template>
      <template #cell-cache_read="{ row }">{{ fmtInt(row.totals.usage.cache_read_tokens) }}</template>
      <template #cell-cache_write="{ row }">{{ fmtInt(row.totals.usage.cache_write_tokens) }}</template>
      <template #cell-errors="{ row }">
        <span :class="row.totals.errors ? 'text-destructive' : ''">{{ fmtInt(row.totals.errors) }}</span>
      </template>
    </DataTable>
  </div>
</template>
