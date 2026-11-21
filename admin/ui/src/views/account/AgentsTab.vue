<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import { DownloadIcon, MoreHorizontalIcon, PlusIcon, Trash2Icon } from '@lucide/vue'
import { agentsApi } from '@/api/admin'
import { useAsync, errorMessage } from '@bboxsys/ui'
import type { Agent } from '@bboxsys/ui'
import { fmtDay, shortKey } from '@bboxsys/ui'
import { DataTable, type Column } from '@bboxsys/ui'
import { ConfirmDialog } from '@bboxsys/ui'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

const props = defineProps<{ accountId: string }>()
const router = useRouter()

const agents = useAsync(() => agentsApi.list(), [] as Agent[])
onMounted(() => agents.run())
watch(() => props.accountId, () => agents.run())

const columns: Column<Agent>[] = [
  { key: 'hostname', label: 'Hostname', sortable: true },
  { key: 'host_id', label: 'Host ID', class: 'font-mono text-xs' },
  { key: 'publicKey', label: 'Public key', class: 'font-mono text-xs', value: a => shortKey(a.publicKey ?? '') },
  { key: 'created_at', label: 'Created', sortable: true, value: a => fmtDay(a.created_at) },
]

const pendingDelete = ref<Agent | null>(null)
const confirmOpen = ref(false)

async function doDelete() {
  if (!pendingDelete.value) return
  await agentsApi.remove(pendingDelete.value.id)
  toast.success(`Deleted agent ${pendingDelete.value.hostname}`)
  agents.run()
}

// Generates a full agent configuration (NATS creds + TLS material) and
// downloads it as JSON for installation on the host.
async function downloadConfig(a: Agent) {
  try {
    const cfg = await agentsApi.configuration({ account_id: props.accountId, host_id: a.host_id, hostname: a.hostname })
    const el = document.createElement('a')
    el.href = URL.createObjectURL(new Blob([JSON.stringify(cfg, null, 2)], { type: 'application/json' }))
    el.download = `${a.hostname || a.host_id}-agent.json`
    el.click()
    URL.revokeObjectURL(el.href)
  } catch (e) {
    toast.error(errorMessage(e))
  }
}
</script>

<template>
  <DataTable
    :columns="columns"
    :rows="agents.data.value"
    :row-key="a => a.id"
    :loading="agents.loading.value"
    :error="agents.error.value"
    empty-text="No agents registered."
    search-placeholder="Filter agents..."
  >
    <template #toolbar>
      <Button size="sm" @click="router.push(`/accounts/${accountId}/agents/new`)"><PlusIcon /> New agent</Button>
    </template>
    <template #actions="{ row }">
      <DropdownMenu>
        <DropdownMenuTrigger as-child>
          <Button variant="ghost" size="icon-sm" aria-label="Actions"><MoreHorizontalIcon /></Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem @click="downloadConfig(row)"><DownloadIcon /> Download configuration</DropdownMenuItem>
          <DropdownMenuItem variant="destructive" @click="pendingDelete = row; confirmOpen = true"><Trash2Icon /> Delete</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </template>
  </DataTable>

  <ConfirmDialog
    v-model:open="confirmOpen"
    title="Delete agent?"
    :description="`${pendingDelete?.hostname || pendingDelete?.host_id} will no longer be able to connect.`"
    confirm-text="Delete"
    destructive
    :action="doDelete"
    @error="m => toast.error(m)"
  />
</template>
