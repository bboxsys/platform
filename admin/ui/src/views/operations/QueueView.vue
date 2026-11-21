<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { RefreshCwIcon } from '@lucide/vue'
import { opsApi } from '@/api/admin'
import { useAsync } from '@bboxsys/ui'
import type { QueueRecord } from '@bboxsys/ui'
import { fmtInt, fmtRelative } from '@bboxsys/ui'
import { PageHeader, StatusBadge } from '@bboxsys/ui'
import { DataTable, type Column } from '@bboxsys/ui'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'

const jobs = useAsync(() => opsApi.queueJobs(), [] as QueueRecord[])
const autoRefresh = ref(true)
let timer: number | undefined

onMounted(() => {
  jobs.run()
  timer = window.setInterval(() => { if (autoRefresh.value) jobs.run() }, 10_000)
})
onBeforeUnmount(() => window.clearInterval(timer))

// The service lists oldest first; show the newest at the top.
const rows = computed(() => [...jobs.data.value].reverse())

function stateOf(r: QueueRecord): string {
  return r.status?.state || 'waiting'
}

const counts = computed(() => {
  const c = { total: rows.value.length, running: 0, complete: 0, error: 0, waiting: 0 }
  for (const r of rows.value) {
    const s = stateOf(r)
    if (s === 'started') c.running++
    else if (s === 'complete') c.complete++
    else if (s === 'error' || s === 'cancelled') c.error++
    else c.waiting++
  }
  return c
})

function payloadText(r: QueueRecord): string {
  const p = r.job.payload
  if (p === undefined || p === null) return ''
  return typeof p === 'string' ? p : JSON.stringify(p)
}

function outcome(r: QueueRecord): string {
  if (!r.status) return ''
  if (r.status.errors?.length) return r.status.errors.join('; ')
  return r.status.result
}

const columns: Column<QueueRecord>[] = [
  { key: 'id', label: 'Job', class: 'font-mono text-xs', sortable: true, value: r => r.job.id },
  { key: 'service', label: 'Service', sortable: true, value: r => r.job.service },
  { key: 'name', label: 'Name', sortable: true, value: r => r.job.name },
  { key: 'state', label: 'State', sortable: true, value: stateOf },
  { key: 'payload', label: 'Payload', class: 'font-mono text-xs', value: payloadText },
  { key: 'outcome', label: 'Result', value: outcome },
  { key: 'updated', label: 'Updated', sortable: true, value: r => r.status?.timestamp ?? '' },
]

const stats = computed(() => [
  { label: 'Jobs', value: fmtInt(counts.value.total) },
  { label: 'Waiting', value: fmtInt(counts.value.waiting) },
  { label: 'Running', value: fmtInt(counts.value.running) },
  { label: 'Complete', value: fmtInt(counts.value.complete) },
  { label: 'Failed', value: fmtInt(counts.value.error) },
])
</script>

<template>
  <PageHeader title="Queue" description="Every job the queue service has relayed to workers, with the latest status each worker reported.">
    <label class="flex items-center gap-2 text-sm"><Switch v-model="autoRefresh" /> Auto refresh</label>
    <Button variant="outline" size="sm" @click="jobs.run()"><RefreshCwIcon /> Refresh</Button>
  </PageHeader>

  <div class="grid gap-4 px-6 pb-6">
    <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
      <Card v-for="s in stats" :key="s.label" class="gap-1 py-4">
        <CardHeader class="px-4">
          <CardDescription>{{ s.label }}</CardDescription>
          <CardTitle class="text-xl tabular-nums">{{ s.value }}</CardTitle>
        </CardHeader>
        <CardContent class="px-4 text-xs text-muted-foreground" />
      </Card>
    </div>

    <DataTable
      :columns="columns"
      :rows="rows"
      :row-key="r => r.job.id"
      :loading="jobs.loading.value"
      :error="jobs.error.value"
      empty-text="No jobs have been queued."
      search-placeholder="Filter jobs..."
    >
      <template #cell-state="{ value }">
        <StatusBadge :status="String(value)" />
      </template>
      <template #cell-payload="{ value }">
        <span class="block max-w-xs truncate" :title="String(value)">{{ value }}</span>
      </template>
      <template #cell-outcome="{ row, value }">
        <span :class="row.status?.errors?.length ? 'text-destructive' : ''" class="block max-w-sm truncate" :title="String(value)">{{ value }}</span>
      </template>
      <template #cell-updated="{ value }">{{ value ? fmtRelative(String(value)) : '' }}</template>
    </DataTable>
  </div>
</template>
