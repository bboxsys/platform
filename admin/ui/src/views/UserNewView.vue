<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import { usersApi } from '@/api/admin'
import { errorMessage } from '@bboxsys/ui'
import { ROLES } from '@bboxsys/ui'
import { PageHeader } from '@bboxsys/ui'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Checkbox } from '@/components/ui/checkbox'
import { Card, CardContent } from '@/components/ui/card'

const route = useRoute()
const router = useRouter()
const accountId = computed(() => String(route.params.accountId))

const form = ref({ email: '', firstname: '', lastname: '', password: '', roles: ['user'] as string[] })
const busy = ref(false)

function toggleRole(role: string, on: boolean) {
  const set = new Set(form.value.roles)
  if (on) set.add(role)
  else set.delete(role)
  form.value.roles = [...set]
}

async function submit() {
  busy.value = true
  try {
    const u = await usersApi.create({ ...form.value, account_id: accountId.value })
    toast.success(`Created ${u.email}`)
    router.push(`/accounts/${accountId.value}/users`)
  } catch (e) {
    toast.error(errorMessage(e))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <PageHeader title="New user" description="Create a user with a password. To let someone set their own password, send an invitation instead." />
  <div class="px-6 pb-6">
    <Card class="max-w-xl">
      <CardContent class="pt-6">
        <form class="grid gap-4" @submit.prevent="submit">
          <div class="grid gap-1.5">
            <Label for="email">Email</Label>
            <Input id="email" v-model="form.email" type="email" required autofocus />
          </div>
          <div class="grid grid-cols-2 gap-3">
            <div class="grid gap-1.5">
              <Label for="first">First name</Label>
              <Input id="first" v-model="form.firstname" />
            </div>
            <div class="grid gap-1.5">
              <Label for="last">Last name</Label>
              <Input id="last" v-model="form.lastname" />
            </div>
          </div>
          <div class="grid gap-1.5">
            <Label for="password">Temporary password</Label>
            <Input id="password" v-model="form.password" type="password" required minlength="8" autocomplete="new-password" />
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
            <Button type="submit" :disabled="busy || form.roles.length === 0">{{ busy ? 'Creating' : 'Create user' }}</Button>
          </div>
        </form>
      </CardContent>
    </Card>
  </div>
</template>
