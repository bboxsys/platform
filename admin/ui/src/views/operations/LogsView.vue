<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { watchDebounced } from '@vueuse/core'
import { toast } from 'vue-sonner'
import { RefreshCwIcon, SearchIcon } from '@lucide/vue'
import { opsApi } from '@/api/admin'
import { useAsync, errorMessage } from '@bboxsys/ui'
import { useSessionStore } from '@/stores/session'
import type { LogEntry } from '@bboxsys/ui'
import { fmtDateTime, fmtInt } from '@bboxsys/ui'
import { PageHeader } from '@bboxsys/ui'
import { DataTable, type Column } from '@bboxsys/ui'
import { StatusBadge } from '@bboxsys/ui'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

const ALL = '__all__'
const LEVELS = ['trace', 'debug', 'info', 'warn', 'error', 'fatal']
const PAGE = 100

const session = useSessionStore()
const filters = reactive({ service: '', host: '', level: ALL, search: '' })
const search = ref('')
const follow = ref(false)

const query = computed(() => ({
  service: filters.service || undefined,
  host: filters.host || undefined,
  level: filters.level === ALL ? undefined : filters.level,
  search: filters.search || undefined,
}))

// Infinite scroll: the first page loads with the filters, the table asks
// for the next page (loadMore) as the reader nears the end, and pages are
// appended. Entries are keyed by id, so an offset that shifted because new
// entries arrived in between never shows a row twice.
const rows = ref<LogEntry[]>([])
const loading = ref(false)
const error = ref('')
const count = useAsync(() => opsApi.logsCount(query.value), { count: 0 })
const level = useAsync(() => opsApi.logLevel(), { level: '' })
const maxEntries = useAsync(() => opsApi.logMaxEntries(), { max_entries: 0 })
const hasMore = computed(() => rows.value.length < count.data.value.count)

// Each reload bumps the generation; a page that arrives for an older
// generation (the filters changed while it was in flight) is dropped.
let generation = 0

function fetchPage(offset: number) {
  return opsApi.logs({ ...query.value, limit: PAGE, offset, sort: 'desc' })
}

async function reload() {
  const gen = ++generation
  loading.value = true
  error.value = ''
  count.run()
  try {
    const page = await fetchPage(0)
    if (gen === generation) rows.value = page
  } catch (e) {
    if (gen === generation) error.value = errorMessage(e)
  } finally {
    if (gen === generation) loading.value = false
  }
}

async function loadMore() {
  if (loading.value || !hasMore.value) return
  const gen = generation
  loading.value = true
  try {
    const page = await fetchPage(rows.value.length)
    if (gen !== generation) return
    const seen = new Set(rows.value.map(l => l.id))
    rows.value = [...rows.value, ...page.filter(l => !seen.has(l.id))]
  } catch (e) {
    if (gen === generation) error.value = errorMessage(e)
  } finally {
    if (gen === generation) loading.value = false
  }
}

// Follow prepends what arrived since the last look and keeps everything
// already loaded, so following does not throw away scrolled-in pages.
async function poll() {
  if (loading.value) return
  const gen = generation
  try {
    const page = await fetchPage(0)
    if (gen !== generation) return
    const seen = new Set(rows.value.map(l => l.id))
    const fresh = page.filter(l => !seen.has(l.id))
    if (fresh.length) rows.value = [...fresh, ...rows.value]
    count.run()
  } catch {
    // A missed poll is retried on the next tick.
  }
}

watchDebounced(search, v => { filters.search = v }, { debounce: 300 })
watch(query, reload)

let timer: number | undefined
onMounted(() => {
  reload()
  level.run()
  maxEntries.run()
  timer = window.setInterval(() => { if (follow.value) poll() }, 5000)
})
onBeforeUnmount(() => window.clearInterval(timer))

const services = computed(() => [...new Set(rows.value.map(l => l.service))].sort())
const hosts = computed(() => [...new Set(rows.value.map(l => l.hostname).filter((h): h is string => !!h))].sort())

// The service is the publishing process; the component is the logger inside
// it. A process whose only logger is named after itself adds nothing by
// repeating it (admin_server/admin_server), so show the component only when
// it says something more (admin_server/idp_store, agent/supervisor).
function source(l: LogEntry): string {
  return l.component && l.component !== l.service ? `${l.service}/${l.component}` : l.service
}

// The page is server-paged and sorted newest first, so the table neither
// filters nor sorts; its header row stays in view while the entries scroll.
const columns: Column<LogEntry>[] = [
  { key: 'timestamp', label: 'Time', class: 'w-44 text-muted-foreground tabular-nums' },
  { key: 'level', label: 'Level', class: 'w-16' },
  { key: 'hostname', label: 'Host', class: 'w-32 max-w-32' },
  { key: 'source', label: 'Source', class: 'w-40 max-w-40', value: l => source(l) },
  { key: 'message', label: 'Message', class: 'whitespace-normal' },
]

// Render a structured field value compactly for the key=value chips.
function fieldValue(v: unknown): string {
  if (v === null || v === undefined) return ''
  if (typeof v === 'object') return JSON.stringify(v)
  return String(v)
}

function fieldEntries(fields?: Record<string, unknown>): [string, string][] {
  if (!fields) return []
  return Object.entries(fields).map(([k, v]) => [k, fieldValue(v)])
}

async function setLevel(v: string) {
  try {
    level.data.value = await opsApi.setLogLevel(v)
    toast.success(`Log level set to ${v}`)
  } catch (e) {
    toast.error(errorMessage(e))
  }
}
</script>

<template>
  <PageHeader title="Logs" description="Recent entries from every service, newest first.">
    <template v-if="session.isSuperAdmin && level.data.value.level">
      <span class="text-xs text-muted-foreground">Global level</span>
      <Select :model-value="level.data.value.level" @update:model-value="v => setLevel(String(v))">
        <SelectTrigger class="w-28"><SelectValue /></SelectTrigger>
        <SelectContent>
          <SelectItem v-for="l in LEVELS.slice(0, 5)" :key="l" :value="l">{{ l }}</SelectItem>
        </SelectContent>
      </Select>
    </template>
    <label class="flex items-center gap-2 text-sm"><Switch v-model="follow" /> Follow</label>
    <Button variant="outline" size="sm" @click="reload"><RefreshCwIcon /> Refresh</Button>
  </PageHeader>

  <div class="flex flex-col gap-3 px-6 pb-6">
    <div class="flex flex-wrap items-center gap-2">
      <div class="relative w-72 max-w-full">
        <SearchIcon class="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
        <Input v-model="search" placeholder="Search messages..." class="pl-8" />
      </div>
      <Select v-model="filters.level">
        <SelectTrigger class="w-32"><SelectValue placeholder="Level" /></SelectTrigger>
        <SelectContent>
          <SelectItem :value="ALL">All levels</SelectItem>
          <SelectItem v-for="l in LEVELS" :key="l" :value="l">{{ l }}</SelectItem>
        </SelectContent>
      </Select>
      <Input v-model="filters.service" placeholder="Service (e.g. admin_server)" class="w-52" list="log-services" />
      <datalist id="log-services"><option v-for="s in services" :key="s" :value="s" /></datalist>
      <Input v-model="filters.host" placeholder="Host" class="w-44" list="log-hosts" />
      <datalist id="log-hosts"><option v-for="h in hosts" :key="h" :value="h" /></datalist>
      <div class="ml-auto flex items-center gap-2 text-xs text-muted-foreground">
        <span class="tabular-nums">{{ fmtInt(rows.length) }} of {{ fmtInt(count.data.value.count) }} entries loaded</span>
        <span v-if="maxEntries.data.value.max_entries">(retaining {{ fmtInt(maxEntries.data.value.max_entries) }})</span>
      </div>
    </div>

    <!-- Fills the window below the page header and filters, so the page
         itself does not scroll and the column headers stay in view. -->
    <DataTable
      class="font-mono text-xs"
      :columns="columns"
      :rows="rows"
      :row-key="l => String(l.id)"
      :loading="loading"
      :error="error"
      :has-more="hasMore"
      empty-text="No log entries match."
      :searchable="false"
      row-class="align-top"
      max-height="calc(100dvh - 15rem)"
      @load-more="loadMore"
    >
      <template #cell-timestamp="{ row }">{{ fmtDateTime(row.timestamp) }}</template>
      <template #cell-level="{ row }"><StatusBadge :status="row.level" /></template>
      <template #cell-hostname="{ row }">
        <span class="block truncate text-muted-foreground" :title="row.hostname">{{ row.hostname ?? '' }}</span>
      </template>
      <template #cell-source="{ row }">
        <span class="block truncate text-muted-foreground" :title="`${row.service} / ${row.component || row.service}`">{{ source(row) }}</span>
      </template>
      <template #cell-message="{ row }">
        <span class="whitespace-pre-wrap break-words">
          {{ row.message }}
          <span
            v-for="[k, v] in fieldEntries(row.fields)"
            :key="k"
            class="ml-1 inline-flex items-center gap-0.5 rounded bg-muted px-1 py-0.5 align-middle text-[11px] text-muted-foreground"
          >
            <span class="font-semibold">{{ k }}</span>=<span class="break-all">{{ v }}</span>
          </span>
        </span>
      </template>
    </DataTable>
  </div>
</template>
