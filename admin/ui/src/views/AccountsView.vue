<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import { MoreHorizontalIcon, PlusIcon, Trash2Icon } from '@lucide/vue'
import { accountsApi } from '@/api/admin'
import { useAsync } from '@bboxsys/ui'
import { useSessionStore } from '@/stores/session'
import type { Account, AccountStats } from '@bboxsys/ui'
import { fmtDay, shortKey } from '@bboxsys/ui'
import { PageHeader } from '@bboxsys/ui'
import { DataTable, type Column } from '@bboxsys/ui'
import { ConfirmDialog } from '@bboxsys/ui'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

const router = useRouter()
const session = useSessionStore()

const accounts = useAsync(() => accountsApi.list(), [] as Account[])
const stats = useAsync(() => accountsApi.stats(), {} as Record<string, AccountStats>)

onMounted(() => {
  accounts.run()
  stats.run()
})

const columns = computed<Column<Account>[]>(() => [
  { key: 'name', label: 'Name', sortable: true },
  { key: 'description', label: 'Description' },
  { key: 'owner', label: 'Owner', sortable: true },
  { key: 'users', label: 'Users', class: 'text-right', sortable: true, value: a => stats.data.value[a.id]?.user_count ?? 0 },
  { key: 'active', label: 'Active', class: 'text-right', sortable: true, value: a => stats.data.value[a.id]?.active_count ?? 0 },
  { key: 'created_at', label: 'Created', sortable: true, value: a => fmtDay(a.created_at) },
  { key: 'id', label: 'ID', class: 'font-mono text-xs', value: a => shortKey(a.id) },
])

const pendingDelete = ref<Account | null>(null)
const confirmOpen = ref(false)

function askDelete(a: Account) {
  pendingDelete.value = a
  confirmOpen.value = true
}

async function doDelete() {
  if (!pendingDelete.value) return
  await accountsApi.remove(pendingDelete.value.id)
  toast.success(`Deleted account ${pendingDelete.value.name}`)
  accounts.run()
  stats.run()
}
</script>

<template>
  <PageHeader title="Accounts" description="Tenant accounts on this platform. Each account has its own users, services, and agents.">
    <Button size="sm" @click="router.push('/accounts/new')"><PlusIcon /> New account</Button>
  </PageHeader>

  <div class="px-6 pb-6">
    <DataTable
      :columns="columns"
      :rows="accounts.data.value"
      :row-key="a => a.id"
      :loading="accounts.loading.value"
      :error="accounts.error.value"
      empty-text="No accounts yet."
      clickable
      @row-click="a => router.push(`/accounts/${a.id}`)"
    >
      <template #cell-name="{ row }">
        <span class="font-medium">{{ row.name }}</span>
        <Badge v-if="row.id === session.accountId" variant="secondary" class="ml-2">yours</Badge>
      </template>
      <template #actions="{ row }">
        <DropdownMenu>
          <DropdownMenuTrigger as-child>
            <Button variant="ghost" size="icon-sm" aria-label="Actions"><MoreHorizontalIcon /></Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem @click="router.push(`/accounts/${row.id}`)">Open</DropdownMenuItem>
            <DropdownMenuItem variant="destructive" :disabled="row.id === session.accountId" @click="askDelete(row)">
              <Trash2Icon /> Delete
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </template>
    </DataTable>
  </div>

  <ConfirmDialog
    v-model:open="confirmOpen"
    title="Delete account?"
    :description="`This permanently deletes ${pendingDelete?.name} and every user, service, and agent in it.`"
    confirm-text="Delete"
    destructive
    :action="doDelete"
    @error="m => toast.error(m)"
  />
</template>
