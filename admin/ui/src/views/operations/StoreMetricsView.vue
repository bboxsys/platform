<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { RefreshCwIcon } from '@lucide/vue'
import { opsApi } from '@/api/admin'
import { useAsync } from '@bboxsys/ui'
import type { StoreMetricsEntry } from '@bboxsys/ui'
import { fmtBytes, fmtInt, fmtPct, fmtRelative } from '@bboxsys/ui'
import { PageHeader } from '@bboxsys/ui'
import { DataTable, type Column } from '@bboxsys/ui'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { Progress } from '@/components/ui/progress'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'

const latest = useAsync(() => opsApi.storeMetrics(), [] as StoreMetricsEntry[])
const autoRefresh = ref(true)
let timer: number | undefined

onMounted(() => {
  latest.run()
  timer = window.setInterval(() => { if (autoRefresh.value) latest.run() }, 30_000)
})
onBeforeUnmount(() => window.clearInterval(timer))

const totals = computed(() => {
  const rows = latest.data.value
  const size = rows.reduce((n, r) => n + r.metrics.size_total, 0)
  const keys = rows.reduce((n, r) => n + r.metrics.num_keys, 0)
  const ops = rows.reduce((n, r) => n + r.metrics.put_ops + r.metrics.get_ops + r.metrics.delete_ops, 0)
  const disk = rows.find(r => r.metrics.disk_total > 0)?.metrics
  return { size, keys, ops, disk, stores: rows.length }
})

const columns: Column<StoreMetricsEntry>[] = [
  { key: 'service', label: 'Service', sortable: true },
  { key: 'store_name', label: 'Store', sortable: true },
  { key: 'hostname', label: 'Host', sortable: true },
  { key: 'size', label: 'Size', class: 'text-right', sortable: true, value: r => r.metrics.size_total },
  { key: 'keys', label: 'Keys', class: 'text-right', sortable: true, value: r => r.metrics.num_keys },
  { key: 'tables', label: 'Tables', class: 'text-right', sortable: true, value: r => r.metrics.num_tables },
  { key: 'cache', label: 'Cache hit', class: 'text-right', value: r => {
    const t = r.metrics.block_cache_hits + r.metrics.block_cache_misses
    return t ? (r.metrics.block_cache_hits / t) * 100 : 0
  } },
  { key: 'ops', label: 'Put / Get / Del', class: 'text-right', value: r => `${fmtInt(r.metrics.put_ops)} / ${fmtInt(r.metrics.get_ops)} / ${fmtInt(r.metrics.delete_ops)}` },
  { key: 'timestamp', label: 'Reported', sortable: true },
]
</script>

<template>
  <PageHeader title="Store metrics" description="Latest snapshot from every BadgerDB store, published over NATS by each service.">
    <label class="flex items-center gap-2 text-sm"><Switch v-model="autoRefresh" /> Auto refresh</label>
    <Button variant="outline" size="sm" @click="latest.run()"><RefreshCwIcon /> Refresh</Button>
  </PageHeader>

  <div class="grid gap-4 px-6 pb-6">
    <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
      <Card class="gap-1 py-4">
        <CardHeader class="px-4"><CardDescription>Stores</CardDescription><CardTitle class="text-xl tabular-nums">{{ totals.stores }}</CardTitle></CardHeader>
      </Card>
      <Card class="gap-1 py-4">
        <CardHeader class="px-4"><CardDescription>Total size</CardDescription><CardTitle class="text-xl tabular-nums">{{ fmtBytes(totals.size) }}</CardTitle></CardHeader>
        <CardContent class="px-4 text-xs text-muted-foreground">{{ fmtInt(totals.keys) }} keys</CardContent>
      </Card>
      <Card class="gap-1 py-4">
        <CardHeader class="px-4"><CardDescription>Operations since start</CardDescription><CardTitle class="text-xl tabular-nums">{{ fmtInt(totals.ops) }}</CardTitle></CardHeader>
      </Card>
      <Card class="gap-1 py-4">
        <CardHeader class="px-4">
          <CardDescription>Disk</CardDescription>
          <CardTitle class="text-xl tabular-nums">{{ totals.disk ? fmtPct(totals.disk.disk_used_pct) : '-' }}</CardTitle>
        </CardHeader>
        <CardContent class="px-4 text-xs text-muted-foreground">
          <Progress v-if="totals.disk" :model-value="totals.disk.disk_used_pct" class="mb-1 h-1.5" />
          <template v-if="totals.disk">{{ fmtBytes(totals.disk.disk_free) }} free of {{ fmtBytes(totals.disk.disk_total) }}</template>
        </CardContent>
      </Card>
    </div>

    <DataTable
      :columns="columns"
      :rows="latest.data.value"
      :row-key="r => `${r.service}:${r.store_name}:${r.hostname}`"
      :loading="latest.loading.value"
      :error="latest.error.value"
      empty-text="No metrics reported yet."
      search-placeholder="Filter stores..."
    >
      <template #cell-size="{ value }">{{ fmtBytes(Number(value)) }}</template>
      <template #cell-keys="{ value }">{{ fmtInt(Number(value)) }}</template>
      <template #cell-cache="{ value }">{{ fmtPct(Number(value)) }}</template>
      <template #cell-timestamp="{ value }">{{ fmtRelative(Number(value)) }}</template>
    </DataTable>
  </div>
</template>
