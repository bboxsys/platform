<script setup lang="ts">
import { computed, onMounted, watch } from 'vue'
import { RouterView, useRoute } from 'vue-router'
import { accountsApi } from '@/api/admin'
import { useAsync } from '@bboxsys/ui'
import { useSessionStore } from '@/stores/session'
import type { Account } from '@bboxsys/ui'
import { fmtDay } from '@bboxsys/ui'
import { PageHeader, PageTabs } from '@bboxsys/ui'
import { Badge } from '@/components/ui/badge'
import { Alert, AlertDescription } from '@/components/ui/alert'

const route = useRoute()
const session = useSessionStore()
const accountId = computed(() => String(route.params.accountId))

// Super admins list all accounts and pick this one; tenant admins can only
// ask for their own.
const account = useAsync(async (): Promise<Account | null> => {
  if (session.isSuperAdmin) {
    const all = await accountsApi.list()
    return all.find(a => a.id === accountId.value) ?? null
  }
  return accountsApi.me()
}, null)

onMounted(() => account.run())
watch(accountId, () => account.run())

const tabs = computed(() => {
  const base = `/accounts/${accountId.value}`
  const t = [
    { to: `${base}/users`, label: 'Users' },
    { to: `${base}/invitations`, label: 'Invitations' },
    { to: `${base}/sso`, label: 'Identity provider' },
  ]
  if (session.isSuperAdmin) {
    t.push({ to: `${base}/services`, label: 'Services' }, { to: `${base}/agents`, label: 'Agents' })
  }
  return t
})
</script>

<template>
  <PageHeader
    :title="account.data.value?.name ?? 'Account'"
    :description="account.data.value?.description || undefined"
  >
    <Badge v-if="account.data.value?.owner" variant="secondary">Owner: {{ account.data.value.owner }}</Badge>
    <Badge v-if="account.data.value" variant="outline">Created {{ fmtDay(account.data.value.created_at) }}</Badge>
  </PageHeader>

  <div v-if="account.error.value" class="px-6 pb-4">
    <Alert variant="destructive"><AlertDescription>{{ account.error.value }}</AlertDescription></Alert>
  </div>

  <PageTabs :tabs="tabs" label="Account sections" class="px-6" />

  <div class="px-6 py-5">
    <RouterView :account-id="accountId" />
  </div>
</template>
