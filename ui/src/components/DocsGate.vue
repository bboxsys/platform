<script setup lang="ts">
import { onMounted, ref } from 'vue'
import type { RequestAuth } from '../lib/http'
import { DEFAULT_DOCS_BASE, docsTarget, openDocsSession } from '../lib/docsSession'
import { Alert, AlertDescription } from '../ui/alert'
import { Button } from '../ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '../ui/card'

// DocsGate is the page the documentation server sends a browser to when it
// has no session: it establishes one with the signed-in user's bearer and
// continues to the page that was asked for. An app mounts it on a route
// behind its own sign-in guard (so an anonymous visitor signs in first and
// lands back here) and passes the route's `next` query parameter:
//
//   { path: '/session/docs', component: DocsGateView }   // wraps DocsGate
//
// The kit does not depend on vue-router, so the app reads the query and
// hands it over.
const props = withDefaults(defineProps<{
  // The session's bearer and account, normally session.auth.
  auth: RequestAuth
  // Where the documentation is served. Defaults to '/docs'.
  base?: string
  // The page to continue to, from the server's redirect; anything not under
  // the base opens the site root.
  next?: string
  title?: string
  // Replaces the navigation, for tests and embedding.
  navigate?: (url: string) => void
}>(), {
  base: DEFAULT_DOCS_BASE,
  next: '',
  title: 'Documentation',
  navigate: undefined,
})

const emit = defineEmits<{ error: [message: string] }>()

const error = ref('')
const busy = ref(false)

async function go() {
  error.value = ''
  busy.value = true
  try {
    await openDocsSession(props.auth, props.base)
    const target = docsTarget(props.next, props.base)
    if (props.navigate) props.navigate(target)
    else window.location.replace(target)
  } catch (e) {
    const status = (e as { status?: number }).status
    error.value = status === 403
      ? 'Your account is not allowed to read the documentation.'
      : status === 401
        ? 'Your session has expired. Sign in again to open the documentation.'
        : 'The documentation is not available right now.'
    emit('error', error.value)
  } finally {
    busy.value = false
  }
}

onMounted(go)
</script>

<template>
  <div class="flex min-h-[60vh] items-center justify-center p-4" data-slot="docs-gate">
    <Card class="w-full max-w-sm">
      <CardHeader>
        <CardTitle>{{ title }}</CardTitle>
        <CardDescription v-if="!error">Opening the documentation...</CardDescription>
      </CardHeader>
      <CardContent v-if="error" class="space-y-3">
        <Alert variant="destructive">
          <AlertDescription>{{ error }}</AlertDescription>
        </Alert>
        <Button :disabled="busy" @click="go">Try again</Button>
      </CardContent>
    </Card>
  </div>
</template>
