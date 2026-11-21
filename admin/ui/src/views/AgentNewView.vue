<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import { agentsApi } from '@/api/admin'
import { errorMessage } from '@bboxsys/ui'
import { PageHeader } from '@bboxsys/ui'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent } from '@/components/ui/card'

const route = useRoute()
const router = useRouter()
const accountId = computed(() => String(route.params.accountId))
const form = ref({ hostname: '', host_id: '' })
const busy = ref(false)

async function submit() {
  busy.value = true
  try {
    const a = await agentsApi.create({ ...form.value, account_id: accountId.value })
    toast.success(`Registered agent ${a.hostname || a.host_id}`)
    router.push(`/accounts/${accountId.value}/agents`)
  } catch (e) {
    toast.error(errorMessage(e))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <PageHeader title="New agent" description="Register a host agent. After creating it, download its configuration from the agents list." />
  <div class="px-6 pb-6">
    <Card class="max-w-xl">
      <CardContent class="pt-6">
        <form class="grid gap-4" @submit.prevent="submit">
          <div class="grid gap-1.5">
            <Label for="hostname">Hostname</Label>
            <Input id="hostname" v-model="form.hostname" required autofocus placeholder="pi-hub.local" />
          </div>
          <div class="grid gap-1.5">
            <Label for="hostid">Host ID</Label>
            <Input id="hostid" v-model="form.host_id" placeholder="Optional stable identifier (machine id, serial)" />
          </div>
          <div class="flex justify-end gap-2">
            <Button type="button" variant="outline" @click="router.back()">Cancel</Button>
            <Button type="submit" :disabled="busy || !form.hostname">{{ busy ? 'Creating' : 'Create agent' }}</Button>
          </div>
        </form>
      </CardContent>
    </Card>
  </div>
</template>
