<script setup lang="ts">
import { computed, onMounted } from 'vue'
import ApiExplorer, { type ApiService } from './api-explorer/ApiExplorer.vue'
import type { SessionStore } from '../session/createSessionStore'

// ServiceApiExplorer lists every service in the session's endpoint map that
// serves /api/v1/openapi.json and opens it in the ApiExplorer with the
// session's credentials. It is the body of each app's Utilities page.
const props = withDefaults(defineProps<{
  session: SessionStore
  // Services listed ahead of the endpoint map, e.g. the same-origin admin
  // service in the admin console.
  include?: ApiService[]
  // Endpoint kinds to skip. The login service has no OpenAPI document and the
  // controller speaks NATS, not HTTP.
  exclude?: string[]
  // Endpoint kind sorted first, typically the app's own service.
  primary?: string
}>(), {
  include: () => [],
  exclude: () => ['login_service', 'controller_service'],
  primary: '',
})

onMounted(() => props.session.bootstrapEndpoints())

const services = computed<ApiService[]>(() => {
  const out: ApiService[] = []
  for (const [kind, ep] of Object.entries(props.session.endpoints)) {
    if (props.exclude.includes(kind)) continue
    if (!ep.url.startsWith('http') && !ep.url.startsWith('ws')) continue
    const base = ep.url.replace(/^wss:\/\//, 'https://').replace(/^ws:\/\//, 'http://')
    out.push({ name: kind, baseUrl: base, specUrl: `${base}/api/v1/openapi.json` })
  }
  out.sort((a, b) => {
    if (a.name === props.primary) return -1
    if (b.name === props.primary) return 1
    return a.name.localeCompare(b.name)
  })
  return [...props.include, ...out]
})

function authHeaders(): Record<string, string> {
  const h: Record<string, string> = {}
  const { token, accountId } = props.session.auth
  if (token) {
    h.Authorization = `Bearer ${token}`
    if (accountId) h['X-Blackbox-AccountID'] = accountId
  }
  return h
}
</script>

<template>
  <div class="h-full min-h-0">
    <ApiExplorer :services="services" :auth-headers="authHeaders" />
  </div>
</template>
