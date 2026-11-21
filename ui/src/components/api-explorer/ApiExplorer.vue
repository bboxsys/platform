<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ChevronRightIcon, PlayIcon, SearchIcon } from '@lucide/vue'
import { httpStatusText } from '../../lib/httpStatus'
import { Input } from '../../ui/input'
import { Badge } from '../../ui/badge'
import { Button } from '../../ui/button'
import { Textarea } from '../../ui/textarea'
import { Alert, AlertDescription } from '../../ui/alert'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../../ui/select'
import {
  exampleFor, listEndpoints, METHOD_TONES, resolveParams, schemaLabel, type EndpointRow, type OpenAPISpec,
} from './openapi'

export interface ApiService {
  // Display name, e.g. "admin_service".
  name: string
  // Base URL for calls, '' for same-origin.
  baseUrl: string
  // URL of the service's openapi.json (absolute or relative to baseUrl).
  specUrl: string
}

const props = defineProps<{
  services: ApiService[]
  // Auth headers added to every "try it" request (Authorization, X-Blackbox-AccountID).
  authHeaders?: () => Record<string, string>
}>()

const serviceName = ref(props.services[0]?.name ?? '')
const service = computed(() => props.services.find(s => s.name === serviceName.value) ?? null)

// The service list may be populated asynchronously by the parent. When it
// arrives (or changes) and nothing valid is selected, default to the first
// service so the explorer does not stay stuck on "Loading spec...".
watch(() => props.services, svcs => {
  if (!service.value && svcs.length) serviceName.value = svcs[0]!.name
}, { immediate: true })

const spec = ref<OpenAPISpec | null>(null)
const specError = ref('')
const search = ref('')
const selectedId = ref<string | null>(null)

// Generation counter so that if the selected service changes while a spec
// fetch is in flight, the stale response is dropped instead of overwriting the
// current service's spec (which would show the wrong endpoints for the URL).
let specGen = 0

async function loadSpec() {
  const g = ++specGen
  spec.value = null
  specError.value = ''
  selectedId.value = null
  const svc = service.value
  if (!svc) return
  try {
    const res = await fetch(svc.specUrl, { headers: props.authHeaders?.() ?? {} })
    if (!res.ok) throw new Error(`${res.status} fetching ${svc.specUrl}`)
    const json = (await res.json()) as OpenAPISpec
    if (g !== specGen) return
    spec.value = json
  } catch (e) {
    if (g !== specGen) return
    specError.value = (e as Error).message
  }
}

watch(service, loadSpec, { immediate: true })

const endpoints = computed(() => (spec.value ? listEndpoints(spec.value) : []))

const grouped = computed(() => {
  const q = search.value.trim().toLowerCase()
  const rows = q
    ? endpoints.value.filter(r =>
        r.path.toLowerCase().includes(q)
        || r.method.toLowerCase().includes(q)
        || (r.op.summary ?? '').toLowerCase().includes(q)
        || r.tag.toLowerCase().includes(q))
    : endpoints.value
  const groups = new Map<string, EndpointRow[]>()
  for (const r of rows) {
    const list = groups.get(r.tag) ?? []
    list.push(r)
    groups.set(r.tag, list)
  }
  return [...groups.entries()]
})

const selected = computed(() => endpoints.value.find(e => e.id === selectedId.value) ?? null)
const selectedParams = computed(() => (spec.value && selected.value ? resolveParams(spec.value, selected.value.op) : []))

// Try-it state, rebuilt when the selection changes.
const paramValues = ref<Record<string, string>>({})
const bodyText = ref('')

watch(selected, sel => {
  paramValues.value = {}
  bodyText.value = ''
  if (!sel || !spec.value) return
  for (const p of selectedParams.value) {
    if (p.example !== undefined) paramValues.value[`${p.in}:${p.name}`] = String(p.example)
    else if (p.schema?.default !== undefined) paramValues.value[`${p.in}:${p.name}`] = String(p.schema.default)
  }
  const content = sel.op.requestBody?.content ?? {}
  const json = content['application/json']
  if (json?.schema) bodyText.value = JSON.stringify(exampleFor(spec.value, json.schema), null, 2)
})

const running = ref(false)
const result = ref<{ status: number; ms: number; contentType: string; body: string } | null>(null)
const runError = ref('')

async function run() {
  const sel = selected.value
  const svc = service.value
  if (!sel || !svc) return
  running.value = true
  runError.value = ''
  result.value = null
  try {
    let path = sel.path
    const query = new URLSearchParams()
    const headers: Record<string, string> = { ...(props.authHeaders?.() ?? {}) }
    for (const p of selectedParams.value) {
      const v = paramValues.value[`${p.in}:${p.name}`]
      if (v === undefined || v === '') {
        if (p.required && p.in === 'path') throw new Error(`Path parameter ${p.name} is required`)
        continue
      }
      if (p.in === 'path') path = path.replace(`{${p.name}}`, encodeURIComponent(v))
      else if (p.in === 'query') query.set(p.name, v)
      else if (p.in === 'header') headers[p.name] = v
    }
    if (path.includes('{')) throw new Error(`Missing path parameter in ${path}`)
    const qs = query.toString()
    const url = `${svc.baseUrl}${path}${qs ? `?${qs}` : ''}`
    const init: RequestInit = { method: sel.method, headers }
    if (bodyText.value.trim() && !['GET', 'HEAD'].includes(sel.method)) {
      headers['Content-Type'] = 'application/json'
      init.body = bodyText.value
    }
    const t0 = performance.now()
    const res = await fetch(url, init)
    const text = await res.text()
    let body = text
    try {
      body = JSON.stringify(JSON.parse(text), null, 2)
    } catch {
      // not JSON; show raw
    }
    if (body.length > 200_000) body = `${body.slice(0, 200_000)}\n... truncated (${body.length} chars)`
    result.value = {
      status: res.status,
      ms: Math.round(performance.now() - t0),
      contentType: res.headers.get('content-type') ?? '',
      body,
    }
  } catch (e) {
    runError.value = (e as Error).message
  } finally {
    running.value = false
  }
}

function responseEntries(row: EndpointRow) {
  return Object.entries(row.op.responses ?? {})
}
</script>

<template>
  <div class="flex h-full min-h-0">
    <div class="flex w-80 shrink-0 flex-col border-r">
      <div class="grid gap-2 border-b p-3">
        <Select v-model="serviceName">
          <SelectTrigger class="w-full"><SelectValue placeholder="Service" /></SelectTrigger>
          <SelectContent>
            <SelectItem v-for="s in services" :key="s.name" :value="s.name">{{ s.name }}</SelectItem>
          </SelectContent>
        </Select>
        <div class="relative">
          <SearchIcon class="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input v-model="search" placeholder="Filter endpoints..." class="pl-8" />
        </div>
      </div>
      <div class="min-h-0 flex-1 overflow-auto p-2">
        <Alert v-if="specError" variant="destructive" class="m-1">
          <AlertDescription>{{ specError }}</AlertDescription>
        </Alert>
        <template v-for="[tag, rows] in grouped" :key="tag">
          <div class="px-2 pb-1 pt-3 text-xs font-medium uppercase tracking-wide text-muted-foreground">{{ tag }}</div>
          <button
            v-for="row in rows"
            :key="row.id"
            type="button"
            class="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs hover:bg-muted"
            :class="selectedId === row.id ? 'bg-muted' : ''"
            @click="selectedId = row.id"
          >
            <Badge variant="outline" :class="['w-14 shrink-0 justify-center font-mono', METHOD_TONES[row.method]]">{{ row.method }}</Badge>
            <span class="truncate font-mono">{{ row.path }}</span>
          </button>
        </template>
        <p v-if="!specError && endpoints.length === 0" class="p-4 text-center text-sm text-muted-foreground">Loading spec...</p>
      </div>
      <div v-if="spec" class="border-t p-3 text-xs text-muted-foreground">
        {{ spec.info.title }} v{{ spec.info.version }} · {{ endpoints.length }} endpoints
      </div>
    </div>

    <div class="min-w-0 flex-1 overflow-auto">
      <div v-if="!selected" class="flex h-full items-center justify-center p-8 text-center text-sm text-muted-foreground">
        <div>
          <p>Select an endpoint to see its documentation and try it against the live service.</p>
          <p v-if="spec?.info.description" class="mx-auto mt-3 max-w-lg text-xs">{{ spec.info.description }}</p>
        </div>
      </div>

      <div v-else class="grid gap-4 p-4">
        <div>
          <div class="flex flex-wrap items-center gap-2">
            <Badge variant="outline" :class="['justify-center font-mono', METHOD_TONES[selected.method]]">{{ selected.method }}</Badge>
            <code class="text-sm font-medium">{{ service?.baseUrl }}{{ selected.path }}</code>
          </div>
          <p v-if="selected.op.summary" class="mt-1 text-sm font-medium">{{ selected.op.summary }}</p>
          <p v-if="selected.op.description" class="mt-1 whitespace-pre-wrap text-xs text-muted-foreground">{{ selected.op.description }}</p>
        </div>

        <div v-if="selectedParams.length" class="grid gap-2">
          <div class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Parameters</div>
          <div v-for="p in selectedParams" :key="`${p.in}:${p.name}`" class="grid grid-cols-[10rem_1fr] items-center gap-3">
            <div class="text-right">
              <code class="text-xs">{{ p.name }}</code>
              <div class="text-[10px] text-muted-foreground">{{ p.in }}{{ p.required ? ' · required' : '' }}</div>
            </div>
            <div>
              <Input v-model="paramValues[`${p.in}:${p.name}`]" :placeholder="p.schema?.type ?? ''" class="font-mono text-xs" />
              <p v-if="p.description" class="mt-0.5 text-[11px] text-muted-foreground">{{ p.description }}</p>
            </div>
          </div>
        </div>

        <div v-if="selected.op.requestBody" class="grid gap-1.5">
          <div class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Request body</div>
          <Textarea v-model="bodyText" rows="8" class="font-mono text-xs" spellcheck="false" />
        </div>

        <div class="flex items-center gap-3">
          <Button size="sm" :disabled="running" @click="run"><PlayIcon /> {{ running ? 'Running' : 'Send request' }}</Button>
          <span v-if="result" class="text-xs tabular-nums text-muted-foreground">
            <Badge :variant="result.status < 400 ? 'secondary' : 'destructive'">{{ result.status }} {{ httpStatusText(result.status) }}</Badge>
            {{ result.ms }} ms · {{ result.contentType }}
          </span>
        </div>

        <Alert v-if="runError" variant="destructive"><AlertDescription>{{ runError }}</AlertDescription></Alert>

        <pre v-if="result" class="max-h-[50vh] overflow-auto rounded-lg border bg-muted/40 p-3 font-mono text-xs leading-relaxed">{{ result.body || '(empty body)' }}</pre>

        <div class="grid gap-1">
          <div class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Responses</div>
          <div v-for="[code, resp] in responseEntries(selected)" :key="code" class="flex items-start gap-2 text-xs">
            <Badge variant="outline" class="font-mono">{{ code }}</Badge>
            <span class="text-muted-foreground">
              {{ httpStatusText(code) }}
              <template v-if="resp.description && resp.description.trim().toLowerCase() !== httpStatusText(code).toLowerCase()">
                {{ httpStatusText(code) ? ' · ' : '' }}{{ resp.description }}
              </template>
              <template v-if="resp.content?.['application/json']?.schema">
                · <ChevronRightIcon class="inline size-3" /> <code>{{ schemaLabel(resp.content['application/json'].schema) }}</code>
              </template>
            </span>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
