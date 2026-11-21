<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import { DownloadIcon, MoreHorizontalIcon, PlusIcon, Trash2Icon } from '@lucide/vue'
import { servicesApi } from '@/api/admin'
import { useAsync } from '@bboxsys/ui'
import type { Service } from '@bboxsys/ui'
import { fmtDay, shortKey } from '@bboxsys/ui'
import { DataTable, type Column } from '@bboxsys/ui'
import { ConfirmDialog } from '@bboxsys/ui'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

const props = defineProps<{ accountId: string }>()
const router = useRouter()

// The services endpoint is scoped by the X-Blackbox-AccountID header, which
// is the caller's own account; cross-account listing is not available.
const services = useAsync(() => servicesApi.list(), [] as Service[])
onMounted(() => services.run())
watch(() => props.accountId, () => services.run())

const columns: Column<Service>[] = [
  { key: 'name', label: 'Name', sortable: true },
  { key: 'roles', label: 'Roles', value: s => (s.roles ?? []).join(', ') },
  { key: 'publicKey', label: 'Public key', class: 'font-mono text-xs', value: s => shortKey(s.publicKey ?? '') },
  { key: 'created_at', label: 'Created', sortable: true, value: s => fmtDay(s.created_at) },
]

const pendingDelete = ref<Service | null>(null)
const confirmOpen = ref(false)

async function doDelete() {
  if (!pendingDelete.value) return
  await servicesApi.remove(pendingDelete.value.id)
  toast.success(`Deleted service ${pendingDelete.value.name}`)
  services.run()
}

function downloadCreds(s: Service) {
  const creds = `-----BEGIN NATS USER JWT-----\n${s.token}\n------END NATS USER JWT------\n\n-----BEGIN USER NKEY SEED-----\n${s.seed}\n------END USER NKEY SEED------\n`
  const a = document.createElement('a')
  a.href = URL.createObjectURL(new Blob([creds], { type: 'text/plain' }))
  a.download = `${s.name}.creds`
  a.click()
  URL.revokeObjectURL(a.href)
}
</script>

<template>
  <DataTable
    :columns="columns"
    :rows="services.data.value"
    :row-key="s => s.id"
    :loading="services.loading.value"
    :error="services.error.value"
    empty-text="No service identities."
    search-placeholder="Filter services..."
  >
    <template #toolbar>
      <Button size="sm" @click="router.push(`/accounts/${accountId}/services/new`)"><PlusIcon /> New service</Button>
    </template>
    <template #cell-roles="{ row }">
      <Badge v-for="r in row.roles" :key="r" variant="outline" class="mr-1 capitalize">{{ r }}</Badge>
    </template>
    <template #actions="{ row }">
      <DropdownMenu>
        <DropdownMenuTrigger as-child>
          <Button variant="ghost" size="icon-sm" aria-label="Actions"><MoreHorizontalIcon /></Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem :disabled="!row.token || !row.seed" @click="downloadCreds(row)"><DownloadIcon /> Download credentials</DropdownMenuItem>
          <DropdownMenuItem variant="destructive" @click="pendingDelete = row; confirmOpen = true"><Trash2Icon /> Delete</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </template>
  </DataTable>

  <ConfirmDialog
    v-model:open="confirmOpen"
    title="Delete service?"
    :description="`Anything authenticating as ${pendingDelete?.name} will be rejected.`"
    confirm-text="Delete"
    destructive
    :action="doDelete"
    @error="m => toast.error(m)"
  />
</template>
