<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import { accountsApi } from '@/api/admin'
import { errorMessage } from '@bboxsys/ui'
import { PageHeader } from '@bboxsys/ui'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Card, CardContent } from '@/components/ui/card'

const router = useRouter()
const form = ref({ name: '', description: '', owner: '' })
const busy = ref(false)

async function submit() {
  busy.value = true
  try {
    const acct = await accountsApi.create(form.value)
    toast.success(`Created account ${acct.name}`)
    router.push(`/accounts/${acct.id}`)
  } catch (e) {
    toast.error(errorMessage(e))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <PageHeader title="New account" description="Create a tenant account. Add users or send invitations after it exists." />
  <div class="px-6 pb-6">
    <Card class="max-w-xl">
      <CardContent class="pt-6">
        <form class="grid gap-4" @submit.prevent="submit">
          <div class="grid gap-1.5">
            <Label for="name">Name</Label>
            <Input id="name" v-model="form.name" required autofocus placeholder="Acme Fishing" />
          </div>
          <div class="grid gap-1.5">
            <Label for="owner">Owner email</Label>
            <Input id="owner" v-model="form.owner" type="email" placeholder="owner@example.com" />
          </div>
          <div class="grid gap-1.5">
            <Label for="desc">Description</Label>
            <Textarea id="desc" v-model="form.description" rows="3" />
          </div>
          <div class="flex justify-end gap-2">
            <Button type="button" variant="outline" @click="router.back()">Cancel</Button>
            <Button type="submit" :disabled="busy || !form.name">{{ busy ? 'Creating' : 'Create account' }}</Button>
          </div>
        </form>
      </CardContent>
    </Card>
  </div>
</template>
