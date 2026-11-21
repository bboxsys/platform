<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { RefreshCwIcon } from '@lucide/vue'
import { pkiApi } from '@/api/admin'
import { useAsync } from '@bboxsys/ui'
import type { CertificateInfo, PKIStatus } from '@bboxsys/ui'
import { fmtDay, isZeroTime } from '@bboxsys/ui'
import { PageHeader } from '@bboxsys/ui'
import { DataTable, type Column } from '@bboxsys/ui'
import { StatusBadge } from '@bboxsys/ui'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'

const status = useAsync(() => pkiApi.status(), null as PKIStatus | null)
const certs = useAsync(() => pkiApi.certificates(), [] as CertificateInfo[])

function load() {
  status.run()
  certs.run()
}
onMounted(load)

const columns: Column<CertificateInfo>[] = [
  { key: 'subject', label: 'Subject', sortable: true },
  { key: 'type', label: 'Type', sortable: true },
  { key: 'status', label: 'Status', sortable: true },
  { key: 'not_after', label: 'Expires', sortable: true, value: c => (isZeroTime(c.not_after) ? '' : c.not_after) },
  { key: 'days_remaining', label: 'Days left', class: 'text-right', sortable: true },
  { key: 'issuer', label: 'Issuer' },
  { key: 'dns_names', label: 'DNS names', value: c => (c.dns_names ?? []).join(', ') },
]

const internal = computed(() => status.data.value?.internal_pki)
</script>

<template>
  <PageHeader title="PKI" description="Certificates issued by the internal CA and by Let's Encrypt.">
    <Button variant="outline" size="sm" @click="load"><RefreshCwIcon /> Refresh</Button>
  </PageHeader>

  <div class="grid gap-4 px-6 pb-6">
    <div class="grid gap-4 sm:grid-cols-2">
      <Card>
        <CardHeader><CardTitle class="text-base">Internal PKI</CardTitle></CardHeader>
        <CardContent class="grid gap-2 text-sm">
          <template v-if="internal">
            <div class="flex items-center justify-between"><span class="text-muted-foreground">CA</span><Badge :variant="internal.has_ca ? 'secondary' : 'destructive'">{{ internal.has_ca ? 'present' : 'missing' }}</Badge></div>
            <div class="flex items-center justify-between"><span class="text-muted-foreground">Server certificate</span><Badge :variant="internal.has_cert ? 'secondary' : 'destructive'">{{ internal.has_cert ? 'present' : 'missing' }}</Badge></div>
            <div class="flex items-center justify-between"><span class="text-muted-foreground">Private key</span><Badge :variant="internal.has_key ? 'secondary' : 'destructive'">{{ internal.has_key ? 'present' : 'missing' }}</Badge></div>
            <div class="mt-1 grid gap-1 font-mono text-xs text-muted-foreground">
              <div class="truncate">{{ internal.ca_file }}</div>
              <div class="truncate">{{ internal.cert_file }}</div>
              <div class="truncate">{{ internal.key_file }}</div>
            </div>
          </template>
          <p v-else-if="status.loading.value" class="text-muted-foreground">Loading</p>
          <p v-else class="text-muted-foreground">{{ status.error.value || 'No internal PKI details reported by this deployment.' }}</p>
        </CardContent>
      </Card>
      <Card>
        <CardHeader><CardTitle class="text-base">Let's Encrypt</CardTitle></CardHeader>
        <CardContent class="text-sm">
          <template v-if="status.data.value?.lets_encrypt?.enabled">
            <div class="grid gap-1">
              <div><span class="text-muted-foreground">Provider:</span> {{ status.data.value.lets_encrypt.provider }}</div>
              <div><span class="text-muted-foreground">Challenge:</span> {{ status.data.value.lets_encrypt.challenge_type }}</div>
              <div><span class="text-muted-foreground">Domains:</span> {{ status.data.value.lets_encrypt.domains.join(', ') }}</div>
            </div>
          </template>
          <p v-else class="text-muted-foreground">Not enabled for this deployment.</p>
        </CardContent>
      </Card>
    </div>

    <DataTable
      :columns="columns"
      :rows="certs.data.value"
      :row-key="c => c.id"
      :loading="certs.loading.value"
      :error="certs.error.value"
      empty-text="No certificates found."
      search-placeholder="Filter certificates..."
    >
      <template #cell-status="{ value }"><StatusBadge :status="String(value)" /></template>
      <template #cell-type="{ value }"><Badge variant="outline">{{ value }}</Badge></template>
      <template #cell-not_after="{ value }">{{ value ? fmtDay(String(value)) : '' }}</template>
      <template #cell-days_remaining="{ row }">{{ isZeroTime(row.not_after) ? '' : row.days_remaining }}</template>
    </DataTable>
  </div>
</template>
