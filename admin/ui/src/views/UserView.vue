<script setup lang="ts">
import { computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import { LockOpenIcon } from '@lucide/vue'
import { usersApi } from '@/api/admin'
import { useAsync, errorMessage } from '@bboxsys/ui'
import type { UserDetail } from '@bboxsys/ui'
import { fmtDateTime, fmtRelative, shortKey } from '@bboxsys/ui'
import { PageHeader } from '@bboxsys/ui'
import { StatusBadge } from '@bboxsys/ui'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Alert, AlertDescription } from '@/components/ui/alert'

const route = useRoute()
const router = useRouter()
const accountId = computed(() => String(route.params.accountId))
const userId = computed(() => String(route.params.userId))

const detail = useAsync(() => usersApi.get(userId.value, accountId.value), null as UserDetail | null)
onMounted(() => detail.run())
watch(userId, () => detail.run())

const user = computed(() => detail.data.value?.user)
const activity = computed(() => detail.data.value?.activity)

async function unlock() {
  try {
    await usersApi.unlock(accountId.value, userId.value)
    toast.success('Account unlocked')
    detail.run()
  } catch (e) {
    toast.error(errorMessage(e))
  }
}

const facts = computed(() => {
  const u = user.value
  const a = activity.value
  if (!u) return []
  return [
    ['Email', u.email],
    ['Name', `${u.firstname ?? ''} ${u.lastname ?? ''}`.trim() || '-'],
    ['User ID', u.id],
    ['Account', shortKey(u.account_id)],
    ['Public key', shortKey(u.publicKey ?? '')],
    ['Created', fmtDateTime(u.created_at)],
    ['OAuth', u.oauth_provider ? `${u.oauth_provider} (${u.oauth_subject})` : 'none'],
    ['Theme', u.theme_preference || 'system'],
    ['Last login', a ? fmtRelative(a.last_login) || 'never' : '-'],
    ['Login count', a ? String(a.login_count) : '-'],
    ['Failed logins', a ? String(a.failed_login_count) : '-'],
    ['Last IP', a?.last_ipv4 || a?.last_ip || '-'],
  ]
})
</script>

<template>
  <PageHeader :title="user?.email ?? 'User'" :description="user ? `${user.firstname ?? ''} ${user.lastname ?? ''}`.trim() : undefined">
    <Button variant="outline" size="sm" @click="router.push(`/accounts/${accountId}/users`)">Back to users</Button>
    <Button v-if="activity?.locked" size="sm" @click="unlock"><LockOpenIcon /> Unlock</Button>
  </PageHeader>

  <div class="grid gap-4 px-6 pb-6 lg:grid-cols-2">
    <Alert v-if="detail.error.value" variant="destructive" class="lg:col-span-2">
      <AlertDescription>{{ detail.error.value }}</AlertDescription>
    </Alert>

    <Card>
      <CardHeader><CardTitle class="text-base">Details</CardTitle></CardHeader>
      <CardContent>
        <dl class="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
          <template v-for="[k, v] in facts" :key="k">
            <dt class="text-muted-foreground">{{ k }}</dt>
            <dd class="min-w-0 truncate font-mono text-xs leading-5">{{ v }}</dd>
          </template>
        </dl>
      </CardContent>
    </Card>

    <Card>
      <CardHeader><CardTitle class="text-base">Access</CardTitle></CardHeader>
      <CardContent class="grid gap-3 text-sm">
        <div>
          <div class="mb-1 text-muted-foreground">Roles</div>
          <Badge v-for="r in user?.roles ?? []" :key="r" variant="outline" class="mr-1 capitalize">{{ r }}</Badge>
        </div>
        <div>
          <div class="mb-1 text-muted-foreground">Status</div>
          <StatusBadge :status="activity?.locked ? 'locked' : 'active'" />
          <p v-if="activity?.locked" class="mt-2 text-xs text-muted-foreground">
            Locked {{ fmtRelative(activity.locked_at) }} after {{ activity.failed_login_count }} failed attempts.
          </p>
        </div>
      </CardContent>
    </Card>
  </div>
</template>
