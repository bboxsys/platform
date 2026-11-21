<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import { CopyIcon } from '@lucide/vue'
import { invitationsApi } from '@/api/admin'
import { errorMessage } from '@bboxsys/ui'
import { adminBase } from '@/stores/session'
import { ROLES, type Invitation } from '@bboxsys/ui'
import { PageHeader } from '@bboxsys/ui'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Checkbox } from '@/components/ui/checkbox'
import { Card, CardContent } from '@/components/ui/card'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'

const route = useRoute()
const router = useRouter()
const accountId = computed(() => String(route.params.accountId))

const form = ref({ email: '', roles: ['user'] as string[], ttl_hours: 72 })
const busy = ref(false)
const created = ref<Invitation | null>(null)

function toggleRole(role: string, on: boolean) {
  const set = new Set(form.value.roles)
  if (on) set.add(role)
  else set.delete(role)
  form.value.roles = [...set]
}

const link = computed(() => created.value
  ? `${adminBase() || window.location.origin}/invite/accept?token=${encodeURIComponent(created.value.token)}`
  : '')

async function submit() {
  busy.value = true
  try {
    created.value = await invitationsApi.create({ ...form.value, account_id: accountId.value })
    toast.success(`Invitation created for ${created.value.email}`)
  } catch (e) {
    toast.error(errorMessage(e))
  } finally {
    busy.value = false
  }
}

async function copy() {
  await navigator.clipboard.writeText(link.value)
  toast.success('Link copied')
}
</script>

<template>
  <PageHeader title="New invitation" description="The invitee picks their own password, or signs in with Google if OAuth is enabled." />
  <div class="px-6 pb-6">
    <Card class="max-w-xl">
      <CardContent class="pt-6">
        <Alert v-if="created" class="mb-4">
          <AlertTitle>Invitation ready</AlertTitle>
          <AlertDescription>
            <p>If SMTP is configured an email has been sent. You can also share this link directly; it expires {{ new Date(created.expires_at).toLocaleString() }}.</p>
            <div class="mt-2 flex items-center gap-2">
              <Input :model-value="link" readonly class="font-mono text-xs" />
              <Button variant="outline" size="icon-sm" aria-label="Copy" @click="copy"><CopyIcon /></Button>
            </div>
          </AlertDescription>
        </Alert>

        <form v-if="!created" class="grid gap-4" @submit.prevent="submit">
          <div class="grid gap-1.5">
            <Label for="email">Email</Label>
            <Input id="email" v-model="form.email" type="email" required autofocus />
          </div>
          <div class="grid gap-1.5">
            <Label for="ttl">Valid for (hours)</Label>
            <Input id="ttl" v-model.number="form.ttl_hours" type="number" min="1" max="720" />
          </div>
          <div class="grid gap-2">
            <Label>Roles</Label>
            <label v-for="r in ROLES" :key="r" class="flex items-center gap-2 text-sm capitalize">
              <Checkbox :model-value="form.roles.includes(r)" @update:model-value="v => toggleRole(r, v === true)" />
              {{ r }}
            </label>
          </div>
          <div class="flex justify-end gap-2">
            <Button type="button" variant="outline" @click="router.back()">Cancel</Button>
            <Button type="submit" :disabled="busy || form.roles.length === 0">{{ busy ? 'Sending' : 'Send invitation' }}</Button>
          </div>
        </form>
        <div v-else class="flex justify-end gap-2">
          <Button variant="outline" @click="created = null; form.email = ''">Invite another</Button>
          <Button @click="router.push(`/accounts/${accountId}/invitations`)">Done</Button>
        </div>
      </CardContent>
    </Card>
  </div>
</template>
