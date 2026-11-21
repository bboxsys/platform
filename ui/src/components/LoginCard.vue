<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { HttpError } from '../lib/http'
import type { SessionStore } from '../session/createSessionStore'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { Label } from '../ui/label'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '../ui/card'
import { Alert, AlertDescription } from '../ui/alert'
import { Separator } from '../ui/separator'

// LoginCard is the sign-in form shared by every bboxsys SPA. It owns the
// email/password fields, the error mapping, and the login service footer;
// the app owns the store, the branding, and what happens after success.
const props = withDefaults(defineProps<{
  session: SessionStore
  title: string
  description?: string
  // Image URL for the mark above the title. Use the "logo" slot instead for
  // an icon component.
  logo?: string
  logoAlt?: string
  // Shows a "Continue with ..." button that emits "oauth" when clicked.
  oauthEnabled?: boolean
  oauthLabel?: string
}>(), {
  description: '',
  logo: '',
  logoAlt: '',
  oauthEnabled: false,
  oauthLabel: 'Continue with Google',
})

const emit = defineEmits<{
  success: []
  oauth: []
}>()

const email = ref('')
const password = ref('')
const error = ref('')
const busy = ref(false)

onMounted(() => props.session.bootstrapEndpoints())

async function submit() {
  error.value = ''
  busy.value = true
  try {
    await props.session.login(email.value.trim(), password.value)
    emit('success')
  } catch (e) {
    if (e instanceof HttpError && e.status === 403) error.value = 'This account is locked. Ask an administrator to unlock it.'
    else if (e instanceof HttpError && e.status === 401) error.value = 'Email or password is incorrect.'
    else error.value = (e as Error).message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="flex min-h-svh items-center justify-center bg-muted/40 p-4">
    <Card class="w-full max-w-sm">
      <CardHeader class="text-center">
        <slot name="logo">
          <img v-if="logo" :src="logo" :alt="logoAlt || title" class="mx-auto mb-2 size-12 rounded-lg" />
        </slot>
        <CardTitle>{{ title }}</CardTitle>
        <CardDescription v-if="description">{{ description }}</CardDescription>
      </CardHeader>
      <CardContent>
        <form class="grid gap-4" @submit.prevent="submit">
          <div class="grid gap-1.5">
            <Label for="email">Email</Label>
            <Input id="email" v-model="email" type="email" autocomplete="username" required autofocus />
          </div>
          <div class="grid gap-1.5">
            <Label for="password">Password</Label>
            <Input id="password" v-model="password" type="password" autocomplete="current-password" required />
          </div>
          <Alert v-if="error" variant="destructive" data-slot="login-error">
            <AlertDescription>{{ error }}</AlertDescription>
          </Alert>
          <Button type="submit" class="w-full" :disabled="busy">{{ busy ? 'Signing in' : 'Sign in' }}</Button>
        </form>
        <template v-if="oauthEnabled">
          <div class="my-4 flex items-center gap-3 text-xs text-muted-foreground">
            <Separator class="flex-1" /> or <Separator class="flex-1" />
          </div>
          <Button variant="outline" class="w-full" @click="emit('oauth')">{{ oauthLabel }}</Button>
        </template>
        <slot />
        <p
          v-if="session.endpoint('login_service')"
          class="mt-4 truncate text-center text-xs text-muted-foreground"
          data-slot="login-service"
        >
          {{ session.endpoint('login_service') }}
        </p>
      </CardContent>
    </Card>
  </div>
</template>
