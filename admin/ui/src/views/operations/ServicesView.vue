<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { RefreshCwIcon } from '@lucide/vue'
import { opsApi } from '@/api/admin'
import { useAsync } from '@bboxsys/ui'
import type { NatsConnInfo, NatsConnz, NatsVarz } from '@bboxsys/ui'
import { fmtBytes, fmtInt, fmtRelative } from '@bboxsys/ui'
import { PageHeader } from '@bboxsys/ui'
import { DataTable, type Column } from '@bboxsys/ui'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Switch } from '@/components/ui/switch'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'

const connz = useAsync(() => opsApi.connz(), null as NatsConnz | null)
const varz = useAsync(() => opsApi.varz(), null as NatsVarz | null)
const autoRefresh = ref(true)
let timer: number | undefined

function load() {
  connz.run()
  varz.run()
}

onMounted(() => {
  load()
  timer = window.setInterval(() => { if (autoRefresh.value) load() }, 10_000)
})
onBeforeUnmount(() => window.clearInterval(timer))

const stats = computed(() => {
  const v = varz.data.value
  if (!v) return []
  return [
    { label: 'Connections', value: fmtInt(v.connections), sub: `${fmtInt(v.total_connections)} total` },
    { label: 'Subscriptions', value: fmtInt(v.subscriptions), sub: `${v.slow_consumers} slow consumers` },
    { label: 'Messages in / out', value: `${fmtInt(v.in_msgs)} / ${fmtInt(v.out_msgs)}`, sub: `${fmtBytes(v.in_bytes)} / ${fmtBytes(v.out_bytes)}` },
    { label: 'Server', value: v.server_name, sub: `NATS ${v.version}, up ${v.uptime}` },
    { label: 'Memory', value: fmtBytes(v.mem), sub: `${v.cores} cores, CPU ${v.cpu}%` },
  ]
})

const columns: Column<NatsConnInfo>[] = [
  { key: 'name', label: 'Client', sortable: true, value: c => c.name || c.name_tag || `cid ${c.cid}` },
  { key: 'account', label: 'Account', class: 'font-mono text-xs', value: c => (c.account ?? '').slice(0, 12) },
  { key: 'ip', label: 'Address', value: c => `${c.ip}:${c.port}` },
  { key: 'lang', label: 'Client lib', value: c => [c.lang, c.version].filter(Boolean).join(' ') },
  { key: 'subscriptions', label: 'Subs', class: 'text-right', sortable: true },
  { key: 'in_msgs', label: 'In', class: 'text-right', sortable: true, value: c => c.in_msgs },
  { key: 'out_msgs', label: 'Out', class: 'text-right', sortable: true, value: c => c.out_msgs },
  { key: 'rtt', label: 'RTT' },
  { key: 'start', label: 'Connected', sortable: true },
]
</script>

<template>
  <PageHeader title="Services" description="Live NATS connections from the controller's monitoring endpoint.">
    <label class="flex items-center gap-2 text-sm"><Switch v-model="autoRefresh" /> Auto refresh</label>
    <Button variant="outline" size="sm" @click="load"><RefreshCwIcon /> Refresh</Button>
  </PageHeader>

  <div class="grid gap-4 px-6 pb-6">
    <div v-if="stats.length" class="grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
      <Card v-for="s in stats" :key="s.label" class="gap-1 py-4">
        <CardHeader class="px-4">
          <CardDescription>{{ s.label }}</CardDescription>
          <CardTitle class="truncate text-xl tabular-nums">{{ s.value }}</CardTitle>
        </CardHeader>
        <CardContent class="px-4 text-xs text-muted-foreground">{{ s.sub }}</CardContent>
      </Card>
    </div>

    <DataTable
      :columns="columns"
      :rows="connz.data.value?.connections ?? []"
      :row-key="c => String(c.cid)"
      :loading="connz.loading.value"
      :error="connz.error.value || varz.error.value"
      empty-text="No client connections."
      search-placeholder="Filter connections..."
    >
      <template #cell-name="{ row, value }">
        <span class="font-medium">{{ value }}</span>
        <Badge v-if="row.kind && row.kind !== 'Client'" variant="outline" class="ml-2">{{ row.kind }}</Badge>
      </template>
      <template #cell-in_msgs="{ value }">{{ fmtInt(Number(value)) }}</template>
      <template #cell-out_msgs="{ value }">{{ fmtInt(Number(value)) }}</template>
      <template #cell-start="{ value }">{{ fmtRelative(String(value)) }}</template>
    </DataTable>
  </div>
</template>
