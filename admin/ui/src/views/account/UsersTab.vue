<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import { DownloadIcon, LockOpenIcon, MoreHorizontalIcon, PlusIcon, Trash2Icon } from '@lucide/vue'
import { usersApi } from '@/api/admin'
import { useAsync, errorMessage } from '@bboxsys/ui'
import { useSessionStore } from '@/stores/session'
import type { Activity, User } from '@bboxsys/ui'
import { fmtRelative } from '@bboxsys/ui'
import { DataTable, type Column } from '@bboxsys/ui'
import { ConfirmDialog } from '@bboxsys/ui'
import { StatusBadge } from '@bboxsys/ui'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

const props = defineProps<{ accountId: string }>()
const router = useRouter()
const session = useSessionStore()

const users = useAsync(() => usersApi.list(props.accountId), [] as User[])
const activities = useAsync(() => usersApi.activities(props.accountId), [] as Activity[])
const activityById = computed(() => new Map(activities.data.value.map(a => [a.id, a])))

function load() {
  users.run()
  activities.run()
}
onMounted(load)
watch(() => props.accountId, load)

const columns: Column<User>[] = [
  { key: 'email', label: 'Email', sortable: true },
  { key: 'name', label: 'Name', sortable: true, value: u => `${u.firstname ?? ''} ${u.lastname ?? ''}`.trim() },
  { key: 'roles', label: 'Roles', value: u => (u.roles ?? []).join(', ') },
  { key: 'status', label: 'Status', value: u => activityById.value.get(u.id)?.locked ? 'locked' : 'active' },
  { key: 'last_login', label: 'Last login', sortable: true, value: u => activityById.value.get(u.id)?.last_login ?? '' },
  { key: 'logins', label: 'Logins', class: 'text-right', sortable: true, value: u => activityById.value.get(u.id)?.login_count ?? 0 },
]

const pendingDelete = ref<User | null>(null)
const confirmOpen = ref(false)

async function doDelete() {
  if (!pendingDelete.value) return
  await usersApi.remove(pendingDelete.value.id)
  toast.success(`Deleted ${pendingDelete.value.email}`)
  load()
}

async function unlock(u: User) {
  try {
    await usersApi.unlock(props.accountId, u.id)
    toast.success(`Unlocked ${u.email}`)
    activities.run()
  } catch (e) {
    toast.error(errorMessage(e))
  }
}

// NATS user credentials in the .creds layout nats CLI and clients accept.
function downloadCreds(u: User) {
  const creds = `-----BEGIN NATS USER JWT-----\n${u.token}\n------END NATS USER JWT------\n\n-----BEGIN USER NKEY SEED-----\n${u.seed}\n------END USER NKEY SEED------\n`
  const blob = new Blob([creds], { type: 'text/plain' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = `${u.email}.creds`
  a.click()
  URL.revokeObjectURL(a.href)
}
</script>

<template>
  <DataTable
    :columns="columns"
    :rows="users.data.value"
    :row-key="u => u.id"
    :loading="users.loading.value"
    :error="users.error.value"
    empty-text="No users in this account."
    search-placeholder="Filter users..."
    clickable
    @row-click="u => router.push(`/accounts/${accountId}/users/${encodeURIComponent(u.id)}`)"
  >
    <template #toolbar>
      <Button size="sm" @click="router.push(`/accounts/${accountId}/users/new`)"><PlusIcon /> New user</Button>
      <Button size="sm" variant="outline" @click="router.push(`/accounts/${accountId}/invitations/new`)">Invite</Button>
    </template>
    <template #cell-email="{ row }">
      <span class="font-medium">{{ row.email }}</span>
      <Badge v-if="row.id === session.user?.id" variant="secondary" class="ml-2">you</Badge>
    </template>
    <template #cell-roles="{ row }">
      <Badge v-for="r in row.roles" :key="r" variant="outline" class="mr-1 capitalize">{{ r }}</Badge>
    </template>
    <template #cell-status="{ value }">
      <StatusBadge :status="String(value)" />
    </template>
    <template #cell-last_login="{ value }">
      {{ fmtRelative(String(value)) }}
    </template>
    <template #actions="{ row }">
      <DropdownMenu>
        <DropdownMenuTrigger as-child>
          <Button variant="ghost" size="icon-sm" aria-label="Actions"><MoreHorizontalIcon /></Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem @click="router.push(`/accounts/${accountId}/users/${encodeURIComponent(row.id)}`)">Open</DropdownMenuItem>
          <DropdownMenuItem v-if="activityById.get(row.id)?.locked" @click="unlock(row)"><LockOpenIcon /> Unlock</DropdownMenuItem>
          <DropdownMenuItem :disabled="!row.token || !row.seed" @click="downloadCreds(row)"><DownloadIcon /> Download credentials</DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem variant="destructive" :disabled="row.id === session.user?.id" @click="pendingDelete = row; confirmOpen = true">
            <Trash2Icon /> Delete
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </template>
  </DataTable>

  <ConfirmDialog
    v-model:open="confirmOpen"
    title="Delete user?"
    :description="`${pendingDelete?.email} will lose access immediately.`"
    confirm-text="Delete"
    destructive
    :action="doDelete"
    @error="m => toast.error(m)"
  />
</template>
