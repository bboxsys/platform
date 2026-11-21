<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import { CopyIcon, MoreHorizontalIcon, PlusIcon, XIcon } from '@lucide/vue'
import { invitationsApi } from '@/api/admin'
import { useAsync } from '@bboxsys/ui'
import { adminBase } from '@/stores/session'
import type { Invitation } from '@bboxsys/ui'
import { fmtRelative, shortUserId } from '@bboxsys/ui'
import { DataTable, type Column } from '@bboxsys/ui'
import { ConfirmDialog } from '@bboxsys/ui'
import { StatusBadge } from '@bboxsys/ui'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

const props = defineProps<{ accountId: string }>()
const router = useRouter()

const invitations = useAsync(() => invitationsApi.list(props.accountId), [] as Invitation[])
onMounted(() => invitations.run())
watch(() => props.accountId, () => invitations.run())

const columns: Column<Invitation>[] = [
  { key: 'email', label: 'Email', sortable: true },
  { key: 'roles', label: 'Roles', value: i => (i.roles ?? []).join(', ') },
  { key: 'status', label: 'Status', sortable: true },
  { key: 'invited_by', label: 'Invited by', value: i => shortUserId(i.invited_by) },
  { key: 'expires_at', label: 'Expires', sortable: true },
  { key: 'created_at', label: 'Created', sortable: true },
]

const pendingRevoke = ref<Invitation | null>(null)
const confirmOpen = ref(false)

async function doRevoke() {
  if (!pendingRevoke.value) return
  await invitationsApi.revoke(pendingRevoke.value.token)
  toast.success(`Revoked invitation for ${pendingRevoke.value.email}`)
  invitations.run()
}

function acceptLink(i: Invitation): string {
  const origin = adminBase() || window.location.origin
  return `${origin}/invite/accept?token=${encodeURIComponent(i.token)}`
}

async function copyLink(i: Invitation) {
  await navigator.clipboard.writeText(acceptLink(i))
  toast.success('Invitation link copied')
}
</script>

<template>
  <DataTable
    :columns="columns"
    :rows="invitations.data.value"
    :row-key="i => i.id"
    :loading="invitations.loading.value"
    :error="invitations.error.value"
    empty-text="No invitations."
    search-placeholder="Filter invitations..."
  >
    <template #toolbar>
      <Button size="sm" @click="router.push(`/accounts/${accountId}/invitations/new`)"><PlusIcon /> New invitation</Button>
    </template>
    <template #cell-roles="{ row }">
      <Badge v-for="r in row.roles" :key="r" variant="outline" class="mr-1 capitalize">{{ r }}</Badge>
    </template>
    <template #cell-status="{ value }"><StatusBadge :status="String(value)" /></template>
    <template #cell-expires_at="{ value }">{{ fmtRelative(String(value)) }}</template>
    <template #cell-created_at="{ value }">{{ fmtRelative(String(value)) }}</template>
    <template #actions="{ row }">
      <DropdownMenu>
        <DropdownMenuTrigger as-child>
          <Button variant="ghost" size="icon-sm" aria-label="Actions"><MoreHorizontalIcon /></Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem :disabled="row.status !== 'pending'" @click="copyLink(row)"><CopyIcon /> Copy link</DropdownMenuItem>
          <DropdownMenuItem variant="destructive" :disabled="row.status !== 'pending'" @click="pendingRevoke = row; confirmOpen = true">
            <XIcon /> Revoke
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </template>
  </DataTable>

  <ConfirmDialog
    v-model:open="confirmOpen"
    title="Revoke invitation?"
    :description="`The link sent to ${pendingRevoke?.email} will stop working.`"
    confirm-text="Revoke"
    destructive
    :action="doRevoke"
    @error="m => toast.error(m)"
  />
</template>
