<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import { servicesApi } from '@/api/admin'
import { errorMessage } from '@bboxsys/ui'
import { PageHeader } from '@bboxsys/ui'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent } from '@/components/ui/card'

const route = useRoute()
const router = useRouter()
const accountId = computed(() => String(route.params.accountId))
const name = ref('')
const busy = ref(false)

async function submit() {
  busy.value = true
  try {
    const s = await servicesApi.create({ name: name.value, account_id: accountId.value })
    toast.success(`Created service ${s.name}`)
    router.push(`/accounts/${accountId.value}/services`)
  } catch (e) {
    toast.error(errorMessage(e))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <PageHeader title="New service" description="A service identity is a NATS user for a backend process. Download its credentials from the services list." />
  <div class="px-6 pb-6">
    <Card class="max-w-xl">
      <CardContent class="pt-6">
        <form class="grid gap-4" @submit.prevent="submit">
          <div class="grid gap-1.5">
            <Label for="name">Service name</Label>
            <Input id="name" v-model="name" required autofocus placeholder="media_server" />
          </div>
          <div class="flex justify-end gap-2">
            <Button type="button" variant="outline" @click="router.back()">Cancel</Button>
            <Button type="submit" :disabled="busy || !name">{{ busy ? 'Creating' : 'Create service' }}</Button>
          </div>
        </form>
      </CardContent>
    </Card>
  </div>
</template>
