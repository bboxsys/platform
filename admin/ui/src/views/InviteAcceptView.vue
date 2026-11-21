<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { invitationsApi, oauthApi } from '@/api/admin'
import { errorMessage } from '@bboxsys/ui'
import { useSessionStore } from '@/stores/session'
import type { PublicInvitation } from '@bboxsys/ui'
import { fmtRelative } from '@bboxsys/ui'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Separator } from '@/components/ui/separator'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Alert, AlertDescription } from '@/components/ui/alert'

const route = useRoute()
const router = useRouter()
const session = useSessionStore()

const token = computed(() => (typeof route.query.token === 'string' ? route.query.token : ''))
const invitation = ref<PublicInvitation | null>(null)
const error = ref('')
const busy = ref(false)
const oauthEnabled = ref(false)
const form = ref({ firstname: '', lastname: '', password: '', confirm: '' })

onMounted(async () => {
  if (!token.value) {
    error.value = 'This link is missing its invitation token.'
    return
  }
  try {
    invitation.value = await invitationsApi.getPublic(token.value)
    if (invitation.value.status !== 'pending') error.value = `This invitation is ${invitation.value.status}.`
  } catch (e) {
    error.value = errorMessage(e)
  }
  try {
    oauthEnabled.value = !!(await oauthApi.config()).enabled
  } catch {
    oauthEnabled.value = false
  }
})

async function accept() {
  if (form.value.password !== form.value.confirm) {
    error.value = 'Passwords do not match.'
    return
  }
  busy.value = true
  error.value = ''
  try {
    const res = await invitationsApi.accept(token.value, {
      firstname: form.value.firstname,
      lastname: form.value.lastname,
      password: form.value.password,
    })
    session.adopt(res)
    router.replace('/')
  } catch (e) {
    error.value = errorMessage(e)
  } finally {
    busy.value = false
  }
}

async function google() {
  try {
    const res = await oauthApi.authorize(token.value)
    sessionStorage.setItem('bboxsys.oauth.state', res.state)
    window.location.href = res.authorization_url
  } catch (e) {
    error.value = errorMessage(e)
  }
}
</script>

<template>
  <div class="flex min-h-svh items-center justify-center bg-muted/40 p-4">
    <Card class="w-full max-w-sm">
      <CardHeader>
        <CardTitle>You're invited</CardTitle>
        <CardDescription v-if="invitation">
          <span v-if="invitation.invited_by_name">{{ invitation.invited_by_name }} invited you</span><span v-else>You were invited</span>
          to join <strong>{{ invitation.account_name || 'an account' }}</strong> as
          <Badge v-for="r in invitation.roles" :key="r" variant="outline" class="ml-1 capitalize">{{ r }}</Badge>.
          Expires {{ fmtRelative(invitation.expires_at) }}.
        </CardDescription>
      </CardHeader>
      <CardContent class="grid gap-4">
        <Alert v-if="error" variant="destructive"><AlertDescription>{{ error }}</AlertDescription></Alert>

        <form v-if="invitation && invitation.status === 'pending'" class="grid gap-3" @submit.prevent="accept">
          <div class="grid gap-1.5">
            <Label>Email</Label>
            <Input :model-value="invitation.email" readonly />
          </div>
          <div class="grid grid-cols-2 gap-3">
            <div class="grid gap-1.5">
              <Label for="first">First name</Label>
              <Input id="first" v-model="form.firstname" autofocus />
            </div>
            <div class="grid gap-1.5">
              <Label for="last">Last name</Label>
              <Input id="last" v-model="form.lastname" />
            </div>
          </div>
          <div class="grid gap-1.5">
            <Label for="pw">Password</Label>
            <Input id="pw" v-model="form.password" type="password" minlength="8" required autocomplete="new-password" />
          </div>
          <div class="grid gap-1.5">
            <Label for="pw2">Confirm password</Label>
            <Input id="pw2" v-model="form.confirm" type="password" minlength="8" required autocomplete="new-password" />
          </div>
          <Button type="submit" :disabled="busy">{{ busy ? 'Creating your account' : 'Accept invitation' }}</Button>
          <template v-if="oauthEnabled">
            <div class="my-1 flex items-center gap-3 text-xs text-muted-foreground"><Separator class="flex-1" /> or <Separator class="flex-1" /></div>
            <Button type="button" variant="outline" @click="google">Continue with Google</Button>
          </template>
        </form>

        <Button v-else-if="error" variant="outline" @click="router.replace('/login')">Go to sign in</Button>
      </CardContent>
    </Card>
  </div>
</template>
