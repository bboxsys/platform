<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { oauthApi } from '@/api/admin'
import { errorMessage } from '@bboxsys/ui'
import { useSessionStore } from '@/stores/session'
import type { LoginResponse, OAuthNewUserResponse } from '@bboxsys/ui'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Alert, AlertDescription } from '@/components/ui/alert'

const route = useRoute()
const router = useRouter()
const session = useSessionStore()

const error = ref('')
const newUser = ref<OAuthNewUserResponse | null>(null)
const account = ref({ name: '', description: '' })
const busy = ref(false)

onMounted(async () => {
  const code = typeof route.query.code === 'string' ? route.query.code : ''
  const state = typeof route.query.state === 'string' ? route.query.state : ''
  if (!code || !state) {
    error.value = 'Missing code or state in the callback URL.'
    return
  }
  const expected = sessionStorage.getItem('bboxsys.oauth.state')
  if (expected && expected !== state) {
    error.value = 'OAuth state mismatch. Start the sign-in again.'
    return
  }
  try {
    const res = await oauthApi.callback(code, state)
    if ('is_new_user' in res && res.is_new_user === true && !('token' in res)) {
      newUser.value = res as OAuthNewUserResponse
      return
    }
    finish(res as LoginResponse)
  } catch (e) {
    error.value = errorMessage(e)
  }
})

function finish(res: LoginResponse) {
  sessionStorage.removeItem('bboxsys.oauth.state')
  session.adopt(res)
  router.replace('/')
}

async function createAccount() {
  if (!newUser.value) return
  busy.value = true
  try {
    finish(await oauthApi.createAccount(account.value.name, account.value.description, newUser.value.oauth_subject))
  } catch (e) {
    error.value = errorMessage(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="flex min-h-svh items-center justify-center bg-muted/40 p-4">
    <Card class="w-full max-w-sm">
      <CardHeader>
        <CardTitle>{{ newUser ? 'Create your account' : 'Signing in' }}</CardTitle>
        <CardDescription v-if="newUser">Welcome, {{ newUser.name || newUser.email }}. Name the account you will administer.</CardDescription>
        <CardDescription v-else-if="!error">Completing Google sign-in.</CardDescription>
      </CardHeader>
      <CardContent class="grid gap-4">
        <Alert v-if="error" variant="destructive"><AlertDescription>{{ error }}</AlertDescription></Alert>
        <form v-if="newUser" class="grid gap-4" @submit.prevent="createAccount">
          <div class="grid gap-1.5">
            <Label for="acct">Account name</Label>
            <Input id="acct" v-model="account.name" required autofocus />
          </div>
          <div class="grid gap-1.5">
            <Label for="desc">Description</Label>
            <Input id="desc" v-model="account.description" />
          </div>
          <Button type="submit" :disabled="busy || !account.name">{{ busy ? 'Creating' : 'Create account' }}</Button>
        </form>
        <Button v-if="error" variant="outline" @click="router.replace('/login')">Back to sign in</Button>
      </CardContent>
    </Card>
  </div>
</template>
