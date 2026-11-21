<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import { LoaderCircleIcon, PencilIcon, PlugZapIcon, PlusIcon, Trash2Icon } from '@lucide/vue'
import { accountTokensApi, aiApi, tokensApi } from '@/api/admin'
import { useSessionStore } from '@/stores/session'
import { useAsync, errorMessage } from '@bboxsys/ui'
import { API_TOKEN_TYPES, type AICredentialCheck, type AICredentialKind, type AIModelOption, type APIToken, type APITokenInput, type APITokenScope, type APITokenType } from '@bboxsys/ui'
import {
  AI_EFFORTS, AI_EMBEDDING_PROVIDERS, AI_PROVIDER_DEFAULT_ENDPOINTS, AI_PROVIDER_LABELS, AI_RESOURCE_TYPE_EMBEDDING,
  AI_RESOURCE_TYPE_MCP_SERVER, CLAUDE_MODELS, DEFAULT_CLAUDE_MODEL, OTHER_MODEL_PROVIDERS,
  aiCredentialLabel, claudeCredentialForm, emptyAICredentialForm, isClaudeCredential, isOtherModelCredential,
  ollamaCredentialForm, readAICredential,
  validateAICredential, withAICredential, type AICredentialForm, type AIProvider,
} from '@bboxsys/ui'
import { fmtRelative } from '@bboxsys/ui'
import { PageHeader, PageTabs } from '@bboxsys/ui'
import { DataTable, type Column } from '@bboxsys/ui'
import { ConfirmDialog } from '@bboxsys/ui'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Checkbox } from '@/components/ui/checkbox'
import { Badge } from '@/components/ui/badge'
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from '@/components/ui/dialog'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

const session = useSessionStore()
const route = useRoute()
const router = useRouter()

// The list endpoint already returns the caller's own tokens plus every
// account-scoped token, each tagged with its scope.
const tokens = useAsync(() => tokensApi.list(), [] as APIToken[])
onMounted(() => tokens.run())

// Every credential is an API token in the vault; the tabs group them by
// what they open. A token belongs to exactly one tab.
type Tab = 'tokens' | 'claude' | 'models' | 'embeddings' | 'mcp'

interface TabSpec {
  label: string
  add: string
  noun: string
  empty: string
}

const TABS: Record<Tab, TabSpec> = {
  tokens:     { label: 'API tokens',  add: 'Add token',         noun: 'API token',           empty: 'No API tokens saved.' },
  claude:     { label: 'Claude',      add: 'Add Claude key',    noun: 'Claude key',          empty: 'No Claude key saved. Add one to use the Assistant.' },
  models:     { label: 'Other models', add: 'Add model',         noun: 'model credential',    empty: 'No Ollama or OpenAI-compatible model saved. Add one to use the Assistant with a local or self-hosted model.' },
  embeddings: { label: 'Embeddings',  add: 'Add embedding key', noun: 'embedding key',       empty: 'No embedding key saved. Add one to enable document search and recalled facts.' },
  mcp:        { label: 'MCP servers', add: 'Add MCP server',    noun: 'MCP server',          empty: 'No MCP servers saved.' },
}
const TAB_ORDER: Tab[] = ['tokens', 'claude', 'models', 'embeddings', 'mcp']

function tabOf(t: APIToken): Tab {
  if (isClaudeCredential(t)) return 'claude'
  if (isOtherModelCredential(t)) return 'models'
  const rt = t.metadata?.resource_type
  if (rt === AI_RESOURCE_TYPE_EMBEDDING) return 'embeddings'
  if (rt === AI_RESOURCE_TYPE_MCP_SERVER) return 'mcp'
  return 'tokens'
}

// The selected tab lives in the query (?tab=claude) so it survives a reload
// and can be linked to, for example from the Assistant's setup notice.
const tab = computed<Tab>({
  get: () => (TAB_ORDER.includes(route.query.tab as Tab) ? route.query.tab as Tab : 'tokens'),
  set: v => { router.replace({ query: { ...route.query, tab: v === 'tokens' ? undefined : v } }) },
})
const spec = computed(() => TABS[tab.value])

const rows = computed(() => tokens.data.value.filter(t => tabOf(t) === tab.value))
const counts = computed(() => {
  const c: Record<Tab, number> = { tokens: 0, claude: 0, models: 0, embeddings: 0, mcp: 0 }
  for (const t of tokens.data.value) c[tabOf(t)]++
  return c
})
const tabItems = computed(() => TAB_ORDER.map(t => ({ value: t, label: TABS[t].label, count: counts.value[t] })))

const COMMON_HEAD: Column<APIToken>[] = [{ key: 'name', label: 'Name', sortable: true }]
const COMMON_TAIL: Column<APIToken>[] = [
  { key: 'scope', label: 'Scope', sortable: true },
  { key: 'has_token', label: 'Secret' },
  { key: 'updated_at', label: 'Updated', sortable: true },
]
// The AI tabs add the result of the last Test press.
const CHECK_COLUMN: Column<APIToken> = { key: 'check', label: 'Test' }
const columns = computed<Column<APIToken>[]>(() => {
  switch (tab.value) {
    case 'claude':
      return [...COMMON_HEAD, { key: 'model', label: 'Model' }, { key: 'effort', label: 'Effort' }, { key: 'endpoint', label: 'Endpoint' }, ...COMMON_TAIL, CHECK_COLUMN]
    case 'models':
      return [...COMMON_HEAD, { key: 'provider', label: 'Provider' }, { key: 'model', label: 'Model' }, { key: 'effort', label: 'Effort' }, { key: 'endpoint', label: 'Endpoint' }, ...COMMON_TAIL, CHECK_COLUMN]
    case 'embeddings':
      return [...COMMON_HEAD, { key: 'provider', label: 'Provider' }, { key: 'model', label: 'Model' }, { key: 'endpoint', label: 'Endpoint' }, ...COMMON_TAIL, CHECK_COLUMN]
    case 'mcp':
      return [...COMMON_HEAD, { key: 'endpoint', label: 'Endpoint' }, { key: 'trust', label: 'Read-only hints' }, ...COMMON_TAIL, CHECK_COLUMN]
    default:
      return [...COMMON_HEAD, { key: 'type', label: 'Provider', sortable: true }, { key: 'metadata', label: 'Resource' }, ...COMMON_TAIL]
  }
})

// Testing a credential. The server resolves the name the way the assistant
// does (the caller's own credential first, then the account's), so a test
// of an account credential shadowed by a same-named user credential tests
// the user's; the result is keyed the same way.
const TAB_KIND: Partial<Record<Tab, AICredentialKind>> = { claude: 'ai_model', models: 'ai_model', embeddings: 'ai_embedding', mcp: 'mcp_server' }
const checks = ref<Record<string, AICredentialCheck>>({})
const testing = ref<Record<string, boolean>>({})
const rowKey = (t: APIToken) => `${t.scope}:${t.name}`

async function testCredential(t: APIToken) {
  const kind = TAB_KIND[tabOf(t)]
  if (!kind) return
  const key = rowKey(t)
  testing.value = { ...testing.value, [key]: true }
  try {
    const res = await aiApi.testCredential(t.name, kind)
    checks.value = { ...checks.value, [key]: res }
    if (res.ok) toast.success(`${t.name}: ${res.message}`, { description: `${res.latency_ms} ms` })
    else toast.error(`${t.name}: ${res.message}`)
  } catch (e) {
    toast.error(errorMessage(e))
  } finally {
    const { [key]: _, ...rest } = testing.value
    testing.value = rest
  }
}

// A saved change makes an earlier result meaningless.
function forgetCheck(name: string) {
  checks.value = Object.fromEntries(Object.entries(checks.value).filter(([k]) => !k.endsWith(`:${name}`)))
}

const CHECK_FAILURE_LABELS: Record<string, string> = {
  auth: 'Key rejected',
  not_found: 'Model not found',
  unreachable: 'Unreachable',
  rejected: 'Failed',
  unsupported: 'Not testable',
}

function apiFor(scope: APITokenScope) {
  return scope === 'account' ? accountTokensApi : tokensApi
}

// A member can manage their own tokens; only an administrator can manage the
// account's shared ones. The server enforces this too.
function canManage(t: APIToken): boolean {
  return t.scope === 'user' || session.canAdminister
}

function resourceLabel(t: APIToken): string {
  const ai = aiCredentialLabel(t)
  if (ai) return ai
  const m = t.metadata ?? {}
  const what = m.resource_name || m.resource_id || ''
  if (!what) return m.endpoint ?? ''
  return m.resource_type ? `${m.resource_type}: ${what}` : what
}

// Display helpers for the AI tabs. An empty value shows what the server
// will use instead, so the table never hides a default.
function modelOf(t: APIToken): string {
  const model = readAICredential(t).model
  if (model) return model
  switch (tabOf(t)) {
    case 'claude': return `${DEFAULT_CLAUDE_MODEL} (default)`
    // The server names no default; Test lists what it offers.
    case 'models': return 'not set'
    default: return 'provider default'
  }
}
function effortOf(t: APIToken): string {
  return readAICredential(t).effort || 'default'
}
function endpointOf(t: APIToken): string {
  const ai = readAICredential(t)
  if (ai.endpoint) return ai.endpoint
  const fallback = AI_PROVIDER_DEFAULT_ENDPOINTS[(ai.provider || 'anthropic') as AIProvider]
  return fallback ? `${fallback} (default)` : ''
}
function providerOf(t: APIToken): string {
  const p = readAICredential(t).provider
  return AI_PROVIDER_LABELS[p as AIProvider] ?? p
}

interface TokenForm {
  name: string
  type: APITokenType
  scope: APITokenScope
  id: string
  token: string
  resource_type: string
  resource_id: string
  resource_name: string
  endpoint: string
  // What the token is for; an AI use writes resource_type, endpoint, and
  // the ai_ tags through the kit helpers instead of the general fields.
  ai: AICredentialForm
}

function formFor(t: Tab): TokenForm {
  const base: TokenForm = {
    name: '', type: 'custom', scope: 'user', id: '', token: '', resource_type: '', resource_id: '', resource_name: '', endpoint: '',
    ai: emptyAICredentialForm(),
  }
  switch (t) {
    case 'claude':
      return { ...base, name: 'claude', type: 'bearer', ai: claudeCredentialForm() }
    case 'models':
      return { ...base, name: 'local', type: 'bearer', ai: ollamaCredentialForm() }
    case 'embeddings':
      return { ...base, name: 'embeddings', type: 'bearer', ai: emptyAICredentialForm({ use: 'ai_embedding', provider: 'voyage', model: 'voyage-3', endpoint: AI_PROVIDER_DEFAULT_ENDPOINTS.voyage ?? '' }) }
    case 'mcp':
      return { ...base, type: 'bearer', ai: emptyAICredentialForm({ use: 'mcp_server' }) }
    default:
      return base
  }
}

const dialogOpen = ref(false)
const dialogTab = ref<Tab>('tokens')
const editing = ref<APIToken | null>(null)
const form = ref<TokenForm>(formFor('tokens'))
const busy = ref(false)
const isEdit = computed(() => editing.value !== null)
const dialogSpec = computed(() => TABS[dialogTab.value])

// Switching the provider of a model or embedding credential moves a
// prefilled endpoint along with it, but leaves an endpoint the user typed
// alone.
watch(() => form.value.ai.provider, (next, prev) => {
  if ((dialogTab.value !== 'embeddings' && dialogTab.value !== 'models') || next === prev) return
  const ai = form.value.ai
  const prevDefault = AI_PROVIDER_DEFAULT_ENDPOINTS[prev as AIProvider] ?? ''
  if (ai.endpoint === '' || ai.endpoint === prevDefault) ai.endpoint = AI_PROVIDER_DEFAULT_ENDPOINTS[next as AIProvider] ?? ''
})

// The model picker lists the current Claude models; "Other model ID" takes
// any id (an older model, a dated snapshot, a gateway's alias). An empty
// model is the server default, which is the first in the list.
const CUSTOM_MODEL = '__custom__'
const customModel = ref(false)
const claudeModelChoice = computed(() => {
  if (customModel.value) return CUSTOM_MODEL
  const id = form.value.ai.model || DEFAULT_CLAUDE_MODEL
  return CLAUDE_MODELS.some(m => m.id === id) ? id : CUSTOM_MODEL
})
function setClaudeModel(v: string) {
  customModel.value = v === CUSTOM_MODEL
  if (v !== CUSTOM_MODEL) form.value.ai.model = v
}
watch(dialogOpen, open => { if (open) customModel.value = false })

// Other models: the backend asks the server what it serves, so the picker
// lists real model ids instead of asking for one. It runs when the dialog
// opens, when the provider changes, when the endpoint or key field is
// left, and on the Find models button; a saved credential's stored key is
// used when none is typed.
const discovered = ref<AIModelOption[]>([])
const discovering = ref(false)
const discoverError = ref('')
const customOtherModel = ref(false)
let discoverSeq = 0

async function discoverModels() {
  const ai = form.value.ai
  if (dialogTab.value !== 'models' || !ai.provider) return
  const endpoint = ai.endpoint.trim()
  if (ai.provider === 'openai_compat' && !endpoint) {
    discovered.value = []
    discoverError.value = ''
    return
  }
  const seq = ++discoverSeq
  discovering.value = true
  discoverError.value = ''
  try {
    const res = await aiApi.listModels({
      provider: ai.provider,
      endpoint: endpoint || undefined,
      token: form.value.token || undefined,
      name: form.value.name || undefined,
    })
    if (seq !== discoverSeq) return
    if (res.ok) {
      discovered.value = res.models
      discoverError.value = res.models.length ? '' : 'The server lists no models.'
      // A server with exactly one model (a llama-server with one file) needs
      // no choice; filling it in saves a click and a credential with no model.
      if (res.models.length === 1 && !ai.model) {
        ai.model = res.models[0].id
        customOtherModel.value = false
      }
    } else {
      discovered.value = []
      discoverError.value = res.message || 'The server did not list its models.'
    }
  } catch (e) {
    if (seq !== discoverSeq) return
    discovered.value = []
    discoverError.value = errorMessage(e)
  } finally {
    if (seq === discoverSeq) discovering.value = false
  }
}

const OTHER_CUSTOM = '__custom__'
const otherModelChoice = computed(() => {
  if (customOtherModel.value) return OTHER_CUSTOM
  const id = form.value.ai.model
  if (!id) return ''
  return discovered.value.some(m => m.id === id) ? id : OTHER_CUSTOM
})
function setOtherModel(v: string) {
  customOtherModel.value = v === OTHER_CUSTOM
  if (v !== OTHER_CUSTOM) form.value.ai.model = v
}
function modelOptionLabel(m: AIModelOption): string {
  const ctx = m.context_window ? ` (${Math.round(m.context_window / 1024)}k context)` : ''
  return `${m.display_name || m.id}${ctx}`
}
watch(dialogOpen, open => {
  discovered.value = []
  discoverError.value = ''
  customOtherModel.value = false
  if (open && dialogTab.value === 'models') discoverModels()
})
watch(() => form.value.ai.provider, (next, prev) => {
  if (dialogOpen.value && dialogTab.value === 'models' && next !== prev) discoverModels()
}, { flush: 'post' })

function resetClaudeEndpoint() {
  form.value.ai.endpoint = AI_PROVIDER_DEFAULT_ENDPOINTS.anthropic ?? ''
}

function openCreate() {
  editing.value = null
  dialogTab.value = tab.value
  form.value = formFor(tab.value)
  dialogOpen.value = true
}

function openEdit(t: APIToken) {
  editing.value = t
  dialogTab.value = tabOf(t)
  const m = t.metadata ?? {}
  form.value = {
    name: t.name, type: t.type, scope: t.scope, id: t.id, token: '',
    resource_type: m.resource_type ?? '', resource_id: m.resource_id ?? '', resource_name: m.resource_name ?? '', endpoint: m.endpoint ?? '',
    ai: readAICredential(t),
  }
  dialogOpen.value = true
}

function toInput(f: TokenForm): APITokenInput {
  const general = f.ai.use === 'general'
  const metadata = {
    resource_type: general && f.resource_type ? f.resource_type : undefined,
    resource_id: general && f.resource_id ? f.resource_id : undefined,
    resource_name: general && f.resource_name ? f.resource_name : undefined,
    endpoint: general && f.endpoint ? f.endpoint : undefined,
  }
  const hasMetadata = Object.values(metadata).some(v => v)
  const input: APITokenInput = {
    name: f.name,
    type: general ? f.type : 'bearer',
    id: f.id || undefined,
    // Empty on edit means "keep the stored secret".
    token: f.token || undefined,
    metadata: hasMetadata ? metadata : undefined,
  }
  // An edit of a token that used to be an AI credential must drop its tags,
  // so the helper runs for every use.
  return withAICredential(input, f.ai)
}

async function save() {
  const problem = validateAICredential(form.value.ai)
  if (problem) {
    toast.error(problem)
    return
  }
  busy.value = true
  try {
    const api = apiFor(form.value.scope)
    if (editing.value) {
      await api.update(editing.value.name, toInput(form.value))
      toast.success(`Updated ${dialogSpec.value.noun} ${form.value.name}`)
    } else {
      await api.create(toInput(form.value))
      toast.success(`Saved ${dialogSpec.value.noun} ${form.value.name}`)
    }
    dialogOpen.value = false
    forgetCheck(form.value.name)
    tokens.run()
  } catch (e) {
    toast.error(errorMessage(e))
  } finally {
    busy.value = false
  }
}

const pendingDelete = ref<APIToken | null>(null)
const confirmOpen = ref(false)

async function doDelete() {
  if (!pendingDelete.value) return
  await apiFor(pendingDelete.value.scope).remove(pendingDelete.value.name)
  tokens.run()
}
</script>

<template>
  <PageHeader title="Credentials" description="Third-party credentials the platform stores on your behalf. User credentials are private to you; account credentials are shared with everyone in the account. Secrets are never shown again after saving.">
    <Button size="sm" @click="openCreate"><PlusIcon /> {{ spec.add }}</Button>
  </PageHeader>

  <div class="px-6 pb-6">
    <PageTabs
      :tabs="tabItems" :model-value="tab" label="Credential kinds" class="mb-4"
      @update:model-value="v => (tab = v as Tab)"
    />

    <DataTable
      :key="tab"
      :columns="columns"
      :rows="rows"
      :row-key="rowKey"
      :loading="tokens.loading.value"
      :error="tokens.error.value"
      :empty-text="spec.empty"
      :searchable="false"
    >
      <template #cell-type="{ value }"><Badge variant="outline">{{ value }}</Badge></template>
      <template #cell-scope="{ value }"><Badge :variant="value === 'account' ? 'secondary' : 'outline'">{{ value }}</Badge></template>
      <template #cell-has_token="{ value }"><span class="text-muted-foreground">{{ value ? 'Stored' : 'Not set' }}</span></template>
      <template #cell-metadata="{ row }"><span class="text-xs text-muted-foreground">{{ resourceLabel(row) }}</span></template>
      <template #cell-model="{ row }"><span class="font-mono text-xs">{{ modelOf(row) }}</span></template>
      <template #cell-effort="{ row }"><span class="text-muted-foreground">{{ effortOf(row) }}</span></template>
      <template #cell-provider="{ row }">{{ providerOf(row) }}</template>
      <template #cell-endpoint="{ row }"><span class="text-xs text-muted-foreground">{{ endpointOf(row) }}</span></template>
      <template #cell-trust="{ row }"><span class="text-muted-foreground">{{ readAICredential(row).trustReadOnly ? 'Trusted' : 'Treated as writes' }}</span></template>
      <template #cell-updated_at="{ value }">{{ fmtRelative(String(value)) }}</template>
      <template #cell-check="{ row }">
        <span v-if="testing[rowKey(row)]" class="text-muted-foreground">Testing</span>
        <span v-else-if="!checks[rowKey(row)]" class="text-muted-foreground">Not tested</span>
        <Badge v-else-if="checks[rowKey(row)].ok" variant="secondary" :title="checks[rowKey(row)].message">
          OK, {{ checks[rowKey(row)].latency_ms }} ms
        </Badge>
        <Badge v-else variant="destructive" :title="checks[rowKey(row)].message">
          {{ CHECK_FAILURE_LABELS[checks[rowKey(row)].error_kind ?? 'rejected'] ?? 'Failed' }}
        </Badge>
      </template>
      <template #actions="{ row }">
        <Button
          v-if="TAB_KIND[tabOf(row)]"
          variant="ghost" size="icon-sm" aria-label="Test" title="Test this credential"
          :disabled="testing[rowKey(row)]" @click="testCredential(row)"
        >
          <LoaderCircleIcon v-if="testing[rowKey(row)]" class="animate-spin" />
          <PlugZapIcon v-else />
        </Button>
        <template v-if="canManage(row)">
          <Button variant="ghost" size="icon-sm" aria-label="Edit" @click="openEdit(row)"><PencilIcon /></Button>
          <Button variant="ghost" size="icon-sm" aria-label="Delete" @click="pendingDelete = row; confirmOpen = true"><Trash2Icon /></Button>
        </template>
      </template>
    </DataTable>
  </div>

  <Dialog v-model:open="dialogOpen">
    <DialogContent>
      <DialogHeader>
        <DialogTitle>{{ isEdit ? `Edit ${dialogSpec.noun}` : dialogSpec.add }}</DialogTitle>
        <DialogDescription>
          <template v-if="dialogTab === 'claude'">
            An Anthropic API key from the Claude Console. The Assistant uses it to call Claude.
          </template>
          <template v-else-if="dialogTab === 'models'">
            A model served by Ollama, llama-server, or any OpenAI-compatible API. The Assistant uses it instead of Claude.
          </template>
          <template v-else-if="dialogTab === 'embeddings'">
            A key for an embeddings API. It enables document search, remember, and recall.
          </template>
          <template v-else-if="dialogTab === 'mcp'">
            A remote MCP server whose tools the Assistant may use.
          </template>
          The secret is encrypted at rest and never returned by the API.
          <template v-if="isEdit"> Leave the secret blank to keep the stored value.</template>
        </DialogDescription>
      </DialogHeader>
      <form id="token-form" class="grid gap-4" @submit.prevent="save">
        <div class="grid grid-cols-2 gap-4">
          <div class="grid gap-1.5">
            <Label for="tname">Name</Label>
            <Input id="tname" v-model="form.name" required :disabled="isEdit" :placeholder="dialogTab === 'tokens' ? 'thunderforest-outdoors' : 'name'" />
          </div>
          <div class="grid gap-1.5">
            <Label>Scope</Label>
            <Select v-model="form.scope" :disabled="isEdit">
              <SelectTrigger class="w-full"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="user">user (only me)</SelectItem>
                <SelectItem value="account" :disabled="!session.canAdminister">account (shared)</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>

        <!-- API tokens: the general vault fields. -->
        <template v-if="dialogTab === 'tokens'">
          <div class="grid grid-cols-2 gap-4">
            <div class="grid gap-1.5">
              <Label>Provider</Label>
              <Select v-model="form.type">
                <SelectTrigger class="w-full"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem v-for="t in API_TOKEN_TYPES" :key="t" :value="t">{{ t }}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div class="grid gap-1.5">
              <Label for="tid">Identifier</Label>
              <Input id="tid" v-model="form.id" placeholder="Email, client ID, or account ID (optional)" autocomplete="off" />
            </div>
          </div>
          <div class="grid gap-1.5">
            <Label for="tvalue">Secret</Label>
            <Input id="tvalue" v-model="form.token" type="password" :required="!isEdit" class="font-mono" autocomplete="off" />
          </div>
          <div class="grid gap-1.5">
            <Label for="tendpoint">Endpoint</Label>
            <Input id="tendpoint" v-model="form.endpoint" placeholder="https://controller.example (optional)" autocomplete="off" />
          </div>
          <div class="grid grid-cols-3 gap-4">
            <div class="grid gap-1.5">
              <Label for="trtype">Resource type</Label>
              <Input id="trtype" v-model="form.resource_type" placeholder="optional" />
            </div>
            <div class="grid gap-1.5">
              <Label for="trid">Resource ID</Label>
              <Input id="trid" v-model="form.resource_id" placeholder="optional" />
            </div>
            <div class="grid gap-1.5">
              <Label for="trname">Resource name</Label>
              <Input id="trname" v-model="form.resource_name" placeholder="optional" />
            </div>
          </div>
        </template>

        <!-- Claude: an Anthropic key, prefilled with the public API and default model. -->
        <template v-else-if="dialogTab === 'claude'">
          <div class="grid gap-1.5">
            <Label for="tclaudekey">API key</Label>
            <Input id="tclaudekey" v-model="form.token" type="password" :required="!isEdit" class="font-mono" placeholder="sk-ant-..." autocomplete="off" />
          </div>
          <div class="grid grid-cols-3 gap-4">
            <div class="col-span-2 grid gap-1.5">
              <Label>Model</Label>
              <Select :model-value="claudeModelChoice" @update:model-value="v => setClaudeModel(String(v))">
                <SelectTrigger class="w-full"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem v-for="m in CLAUDE_MODELS" :key="m.id" :value="m.id">{{ m.label }}</SelectItem>
                  <SelectItem :value="CUSTOM_MODEL">Other model ID...</SelectItem>
                </SelectContent>
              </Select>
              <Input
                v-if="claudeModelChoice === CUSTOM_MODEL"
                id="tclaudemodel" v-model="form.ai.model" placeholder="claude-..." class="font-mono" autocomplete="off"
                aria-label="Model ID"
              />
              <p v-else class="font-mono text-xs text-muted-foreground">{{ form.ai.model || DEFAULT_CLAUDE_MODEL }}</p>
            </div>
            <div class="grid gap-1.5">
              <Label>Effort</Label>
              <Select :model-value="form.ai.effort || '__default__'" @update:model-value="v => form.ai.effort = v === '__default__' ? '' : String(v)">
                <SelectTrigger class="w-full"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="__default__">default (medium)</SelectItem>
                  <SelectItem v-for="e in AI_EFFORTS" :key="e" :value="e">{{ e }}</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <div class="grid gap-1.5">
            <div class="flex items-center justify-between">
              <Label for="tclaudeendpoint">API endpoint</Label>
              <Button
                v-if="form.ai.endpoint !== AI_PROVIDER_DEFAULT_ENDPOINTS.anthropic"
                type="button" variant="link" size="sm" class="h-auto p-0 text-xs" @click="resetClaudeEndpoint"
              >Use the public API</Button>
            </div>
            <Input id="tclaudeendpoint" v-model="form.ai.endpoint" class="font-mono" autocomplete="off" />
            <p class="text-xs text-muted-foreground">
              The Claude API base URL. Change it only to route through a gateway or proxy that speaks the Anthropic API.
            </p>
          </div>
        </template>

        <!-- Other models: Ollama (or llama-server) by its server root, or an
             OpenAI-compatible base URL. The key is optional; local servers
             have none. -->
        <template v-else-if="dialogTab === 'models'">
          <div class="grid grid-cols-[1fr_2fr_1fr] gap-4">
            <div class="grid min-w-0 gap-1.5">
              <Label>Provider</Label>
              <Select v-model="form.ai.provider">
                <SelectTrigger class="w-full"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem v-for="p in OTHER_MODEL_PROVIDERS" :key="p" :value="p">{{ AI_PROVIDER_LABELS[p] }}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div class="grid min-w-0 gap-1.5">
              <Label for="tothermodel">Model</Label>
              <Select v-if="discovered.length" :model-value="otherModelChoice" @update:model-value="v => setOtherModel(String(v))">
                <SelectTrigger id="tothermodel" class="w-full min-w-0 [&>span]:truncate"><SelectValue placeholder="Choose a model" /></SelectTrigger>
                <SelectContent>
                  <SelectItem v-for="m in discovered" :key="m.id" :value="m.id">{{ modelOptionLabel(m) }}</SelectItem>
                  <SelectItem :value="OTHER_CUSTOM">Other model ID...</SelectItem>
                </SelectContent>
              </Select>
              <Input
                v-if="!discovered.length || otherModelChoice === OTHER_CUSTOM"
                id="tothermodelid" v-model="form.ai.model" :placeholder="form.ai.provider === 'ollama' ? 'qwen3:8b' : 'as the server lists it'"
                class="font-mono" autocomplete="off" aria-label="Model ID"
              />
            </div>
            <div class="grid min-w-0 gap-1.5">
              <Label>Effort</Label>
              <Select :model-value="form.ai.effort || '__default__'" @update:model-value="v => form.ai.effort = v === '__default__' ? '' : String(v)">
                <SelectTrigger class="w-full"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="__default__">server default</SelectItem>
                  <SelectItem v-for="e in AI_EFFORTS" :key="e" :value="e">{{ e }}</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <div class="grid gap-1.5">
            <Label for="totherendpoint">{{ form.ai.provider === 'ollama' ? 'Server URL' : 'API endpoint' }}</Label>
            <Input
              id="totherendpoint" v-model="form.ai.endpoint" class="font-mono" autocomplete="off"
              :placeholder="form.ai.provider === 'ollama' ? 'http://localhost:11434' : 'https://llm.example/v1 (required)'"
              @blur="discoverModels"
            />
            <p class="text-xs text-muted-foreground">
              <template v-if="form.ai.provider === 'ollama'">
                The Ollama server root, as OLLAMA_HOST names it. A llama-server root works too.
              </template>
              <template v-else>
                The base URL the server mounts the OpenAI chat API under, usually ending in /v1.
              </template>
            </p>
          </div>
          <div class="grid gap-1.5">
            <Label for="totherkey">API key</Label>
            <Input id="totherkey" v-model="form.token" type="password" class="font-mono" placeholder="optional for a local server" autocomplete="off" @blur="discoverModels" />
          </div>
          <div class="flex items-center justify-between gap-4 text-xs">
            <span v-if="discovering" class="text-muted-foreground">Asking the server for its models</span>
            <span v-else-if="discoverError" class="text-destructive">{{ discoverError }}</span>
            <span v-else-if="discovered.length" class="text-muted-foreground">{{ discovered.length }} {{ discovered.length === 1 ? 'model' : 'models' }} offered by the server.</span>
            <span v-else class="text-muted-foreground">Enter the server URL to list its models.</span>
            <Button type="button" variant="link" size="sm" class="h-auto p-0 text-xs" :disabled="discovering" @click="discoverModels">
              <LoaderCircleIcon v-if="discovering" class="animate-spin" /> Find models
            </Button>
          </div>
        </template>

        <!-- Embeddings: Voyage, an OpenAI-compatible endpoint, or Ollama. Only
             Voyage always needs a key; a local llama-server or Ollama has
             none, as on the Other models tab. -->
        <template v-else-if="dialogTab === 'embeddings'">
          <div class="grid gap-1.5">
            <Label for="tembedkey">API key</Label>
            <Input
              id="tembedkey" v-model="form.token" type="password" :required="!isEdit && form.ai.provider === 'voyage'"
              :placeholder="form.ai.provider === 'voyage' ? '' : 'optional for a local server'" class="font-mono" autocomplete="off"
            />
          </div>
          <div class="grid grid-cols-2 gap-4">
            <div class="grid gap-1.5">
              <Label>Provider</Label>
              <Select v-model="form.ai.provider">
                <SelectTrigger class="w-full"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem v-for="p in AI_EMBEDDING_PROVIDERS" :key="p" :value="p">{{ AI_PROVIDER_LABELS[p] }}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div class="grid gap-1.5">
              <Label for="tembedmodel">Model</Label>
              <Input id="tembedmodel" v-model="form.ai.model" placeholder="voyage-3" class="font-mono" autocomplete="off" />
            </div>
          </div>
          <div class="grid gap-1.5">
            <Label for="tembedendpoint">API endpoint</Label>
            <Input id="tembedendpoint" v-model="form.ai.endpoint" class="font-mono" :placeholder="form.ai.provider === 'openai_compat' ? 'https://llm.example/v1 (required)' : ''" autocomplete="off" />
            <p v-if="form.ai.provider === 'ollama'" class="text-xs text-muted-foreground">The Ollama server root; the embeddings route is added.</p>
          </div>
        </template>

        <!-- MCP servers. -->
        <template v-else>
          <div class="grid gap-1.5">
            <Label for="tmcpendpoint">Server URL</Label>
            <Input id="tmcpendpoint" v-model="form.ai.endpoint" required placeholder="https://mcp.example/mcp" class="font-mono" autocomplete="off" />
            <p class="text-xs text-muted-foreground">The server's streamable HTTP URL. The secret is sent as its bearer.</p>
          </div>
          <div class="grid gap-1.5">
            <Label for="tmcpkey">Bearer token</Label>
            <Input id="tmcpkey" v-model="form.token" type="password" class="font-mono" autocomplete="off" />
          </div>
          <div class="flex items-start gap-2">
            <Checkbox id="taitrust" :model-value="form.ai.trustReadOnly" class="mt-0.5" @update:model-value="v => (form.ai.trustReadOnly = v === true)" />
            <div class="grid gap-1">
              <Label for="taitrust">Trust the server's read-only hints</Label>
              <p class="text-xs text-muted-foreground">
                When off, every tool from this server is treated as a write and needs writes to be allowed.
              </p>
            </div>
          </div>
        </template>
      </form>
      <DialogFooter>
        <Button variant="outline" @click="dialogOpen = false">Cancel</Button>
        <Button type="submit" form="token-form" :disabled="busy">{{ busy ? 'Saving' : 'Save' }}</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>

  <ConfirmDialog
    v-model:open="confirmOpen"
    :title="`Delete ${pendingDelete ? TABS[tabOf(pendingDelete)].noun : 'credential'}?`"
    :description="`${pendingDelete?.name} will be removed. Anything using it will stop working.`"
    confirm-text="Delete"
    destructive
    :action="doDelete"
    @error="m => toast.error(m)"
  />
</template>
