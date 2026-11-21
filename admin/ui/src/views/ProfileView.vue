<script setup lang="ts">
import { computed, ref } from 'vue'
import { useDark } from '@vueuse/core'
import { toast } from 'vue-sonner'
import { meApi } from '@/api/admin'
import { errorMessage } from '@bboxsys/ui'
import { useSessionStore } from '@/stores/session'
import { fmtDateTime, shortKey } from '@bboxsys/ui'
import { PageHeader } from '@bboxsys/ui'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Avatar, AvatarFallback, AvatarImage } from '@/components/ui/avatar'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

const session = useSessionStore()
const isDark = useDark()
const user = computed(() => session.user)

const pw = ref({ current: '', next: '', confirm: '' })
const pwBusy = ref(false)

async function changePassword() {
  if (pw.value.next !== pw.value.confirm) {
    toast.error('New passwords do not match')
    return
  }
  pwBusy.value = true
  try {
    await meApi.changePassword(pw.value.current, pw.value.next)
    toast.success('Password changed')
    pw.value = { current: '', next: '', confirm: '' }
  } catch (e) {
    toast.error(errorMessage(e))
  } finally {
    pwBusy.value = false
  }
}

const theme = computed(() => user.value?.theme_preference || 'system')

async function setTheme(v: string) {
  const t = v as 'system' | 'dark' | 'light'
  try {
    await meApi.setTheme(t)
    session.patchUser({ theme_preference: t })
    if (t === 'dark') isDark.value = true
    else if (t === 'light') isDark.value = false
    else isDark.value = window.matchMedia('(prefers-color-scheme: dark)').matches
  } catch (e) {
    toast.error(errorMessage(e))
  }
}

async function onPhoto(e: Event) {
  const file = (e.target as HTMLInputElement).files?.[0]
  if (!file) return
  try {
    const res = await meApi.uploadPhoto(file)
    session.patchUser({ profile_photo: res.profile_photo })
    toast.success('Photo updated')
  } catch (err) {
    toast.error(errorMessage(err))
  }
}

async function removePhoto() {
  try {
    await meApi.deletePhoto()
    session.patchUser({ profile_photo: '' })
  } catch (e) {
    toast.error(errorMessage(e))
  }
}

const initials = computed(() => {
  const u = user.value
  if (!u) return '?'
  return ((u.firstname?.[0] ?? '') + (u.lastname?.[0] ?? '') || u.email[0]).toUpperCase()
})
</script>

<template>
  <PageHeader title="Profile" />
  <div class="grid gap-4 px-6 pb-6 lg:grid-cols-2">
    <Card>
      <CardHeader>
        <CardTitle class="text-base">Identity</CardTitle>
      </CardHeader>
      <CardContent class="grid gap-4">
        <div class="flex items-center gap-4">
          <Avatar class="size-16">
            <AvatarImage v-if="user?.profile_photo || user?.picture" :src="user.profile_photo || user.picture || ''" />
            <AvatarFallback class="text-lg">{{ initials }}</AvatarFallback>
          </Avatar>
          <div class="grid gap-1">
            <label>
              <span class="sr-only">Upload photo</span>
              <input type="file" accept="image/*" class="hidden" @change="onPhoto">
              <Button variant="outline" size="sm" as="span">Upload photo</Button>
            </label>
            <Button v-if="user?.profile_photo" variant="ghost" size="sm" @click="removePhoto">Remove</Button>
          </div>
        </div>
        <dl class="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
          <dt class="text-muted-foreground">Email</dt><dd>{{ user?.email }}</dd>
          <dt class="text-muted-foreground">Name</dt><dd>{{ user?.firstname }} {{ user?.lastname }}</dd>
          <dt class="text-muted-foreground">Account</dt><dd class="font-mono text-xs">{{ shortKey(session.accountId) }}</dd>
          <dt class="text-muted-foreground">Roles</dt>
          <dd>
            <Badge v-if="session.isSuperAdmin" class="mr-1">Super admin</Badge>
            <Badge v-for="r in user?.roles ?? []" :key="r" variant="outline" class="mr-1 capitalize">{{ r }}</Badge>
          </dd>
          <dt class="text-muted-foreground">Member since</dt><dd>{{ fmtDateTime(user?.created_at) }}</dd>
        </dl>
      </CardContent>
    </Card>

    <div class="grid gap-4">
      <Card>
        <CardHeader>
          <CardTitle class="text-base">Appearance</CardTitle>
          <CardDescription>Stored on your user so it follows you between browsers.</CardDescription>
        </CardHeader>
        <CardContent>
          <Select :model-value="theme" @update:model-value="v => setTheme(String(v))">
            <SelectTrigger class="w-48"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="system">Match system</SelectItem>
              <SelectItem value="light">Light</SelectItem>
              <SelectItem value="dark">Dark</SelectItem>
            </SelectContent>
          </Select>
        </CardContent>
      </Card>

      <Card>
        <CardHeader><CardTitle class="text-base">Change password</CardTitle></CardHeader>
        <CardContent>
          <form class="grid gap-3" @submit.prevent="changePassword">
            <div class="grid gap-1.5">
              <Label for="cur">Current password</Label>
              <Input id="cur" v-model="pw.current" type="password" autocomplete="current-password" required />
            </div>
            <div class="grid gap-1.5">
              <Label for="new">New password</Label>
              <Input id="new" v-model="pw.next" type="password" autocomplete="new-password" minlength="8" required />
            </div>
            <div class="grid gap-1.5">
              <Label for="conf">Confirm new password</Label>
              <Input id="conf" v-model="pw.confirm" type="password" autocomplete="new-password" minlength="8" required />
            </div>
            <div class="flex justify-end">
              <Button type="submit" :disabled="pwBusy">{{ pwBusy ? 'Saving' : 'Change password' }}</Button>
            </div>
          </form>
        </CardContent>
      </Card>
    </div>
  </div>
</template>
