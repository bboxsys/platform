<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ChevronDownIcon, ChevronRightIcon, PencilIcon, PlusIcon, SendIcon, SquareIcon, Trash2Icon, WrenchIcon } from '@lucide/vue'
import type { AIEvent, AISession, AISessionSummary, AIStatus, AIToolCall, AIToolResult, AIUsage } from '../../types/admin'
import type { AssistantApi } from '../../lib/assistantApi'
import { errorMessage, useAsync } from '../../composables/useAsync'
import { fmtInt, fmtRelative } from '../../lib/format'
import { Button } from '../../ui/button'
import { Input } from '../../ui/input'
import { Textarea } from '../../ui/textarea'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../../ui/select'
import AppNotice from '../AppNotice.vue'
import ConfirmDialog from '../ConfirmDialog.vue'
import AssistantMarkdown from './AssistantMarkdown.vue'

// A conversation with ONE assistant: its sessions, the transcript of the
// open one with tool calls and results inline (collapsed), the streamed
// run in progress, token usage, and the composer. The host picks the
// assistant (the api prop is bound to it) and keys this component by that
// choice, so a switch remounts it and no reply can land in another
// assistant's store. Two layouts share every piece of state: `page` lists
// the sessions in a sidebar (the admin console's Assistant page), `panel`
// keeps them in a dropdown above the transcript (a drawer or inspector).
const props = withDefaults(defineProps<{
  api: AssistantApi
  layout?: 'page' | 'panel'
  // Names the assistant in the composer's placeholder.
  serviceLabel?: string
}>(), { layout: 'page', serviceLabel: '' })

const emit = defineEmits<{
  // A failure worth a toast; the host shows it the way its app does.
  error: [message: string]
}>()

// Anything in flight: the host disables its assistant picker on it so a
// switch never races a request made on the previous assistant.
const busy = defineModel<boolean>('busy', { default: false })

// One row of the transcript. A stored session is flattened into these; the
// run in progress appends more as events arrive and is replaced by the
// stored session once the run ends.
type Item =
  | { kind: 'user'; key: string; text: string }
  | { kind: 'assistant'; key: string; text: string }
  | { kind: 'thinking'; key: string; text: string }
  | { kind: 'tool'; key: string; call: AIToolCall; result?: AIToolResult }
  | { kind: 'note'; key: string; text: string; error?: boolean }

const status = useAsync(() => props.api.status(), null as AIStatus | null)
const sessions = useAsync(() => props.api.sessions.list(), [] as AISessionSummary[])
const current = ref<AISession | null>(null)
const loadingSession = ref(false)

// The run in progress.
const live = ref<Item[]>([])
const liveUsage = ref<AIUsage | null>(null)
const sending = ref(false)
let controller: AbortController | null = null
let seq = 0

const draft = ref('')
const expanded = ref(new Set<string>())
const transcript = ref<HTMLElement | null>(null)

onMounted(async () => {
  status.run()
  await sessions.run()
  const newest = sessions.data.value[0]
  if (newest) select(newest.id)
})
onBeforeUnmount(() => controller?.abort())

watch(() => sending.value || loadingSession.value || status.loading.value || sessions.loading.value, v => { busy.value = v }, { immediate: true })

const ready = computed(() => !!status.data.value?.enabled && !!status.data.value?.model)
const unconfigured = computed(() => status.data.value !== null && !status.loading.value && !ready.value)

// Which model credential a new session is created on. The status lists
// every credential the caller may name, the server's default first; the
// choice lives here until a session is made from it, and a stale choice
// (a credential deleted since) falls back to the default.
const models = computed(() => status.data.value?.models ?? [])
const chosenModel = ref('')
const newSessionModel = computed(() => {
  const list = models.value
  if (chosenModel.value && list.some(m => m.name === chosenModel.value)) return chosenModel.value
  return list[0]?.name ?? ''
})
function modelChoiceLabel(m: { name: string; model: string; provider: string }): string {
  return `${m.name}: ${m.model || m.provider}`
}

// The stored transcript: assistant tool calls paired with the results the
// next user message carried back.
const stored = computed<Item[]>(() => {
  const out: Item[] = []
  const calls = new Map<string, Extract<Item, { kind: 'tool' }>>()
  const msgs = current.value?.messages ?? []
  msgs.forEach((m, i) => {
    if (m.role === 'assistant') {
      if (m.text) out.push({ kind: 'assistant', key: `m${i}`, text: m.text })
      for (const c of m.tool_calls ?? []) {
        const item: Extract<Item, { kind: 'tool' }> = { kind: 'tool', key: `c${c.id}`, call: c }
        calls.set(c.id, item)
        out.push(item)
      }
      return
    }
    for (const r of m.tool_results ?? []) {
      const item = calls.get(r.id)
      if (item) item.result = r
      else out.push({ kind: 'tool', key: `r${r.id}`, call: { id: r.id, name: r.name, input: null }, result: r })
    }
    if (m.text) out.push({ kind: 'user', key: `m${i}`, text: m.text })
  })
  return out
})

const items = computed(() => [...stored.value, ...live.value])

// The reply still streaming in: its unfinished chart block shows a
// placeholder rather than a parse error.
function isStreaming(it: Item): boolean {
  return sending.value && live.value.at(-1) === it
}

// A reply with a chart takes the full width of the transcript so the
// chart has room; a text-only reply hugs its content like a chat bubble.
function hasChart(text: string): boolean {
  return /```\s*echarts\b/i.test(text)
}

const usage = computed<AIUsage>(() => {
  const u = { input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, ...current.value?.usage }
  const l = liveUsage.value
  if (l) {
    u.input_tokens += l.input_tokens
    u.output_tokens += l.output_tokens
    u.cache_read_tokens += l.cache_read_tokens
    u.cache_write_tokens += l.cache_write_tokens
  }
  return u
})

watch(() => [items.value.length, live.value.at(-1)?.kind === 'assistant' ? (live.value.at(-1) as { text: string }).text.length : 0], async () => {
  await nextTick()
  const el = transcript.value
  if (el) el.scrollTop = el.scrollHeight
})

function titleOf(s: { title?: string }): string {
  return s.title?.trim() || 'Untitled'
}

// Renaming a session: the pencil (or a double-click on the title in the
// page layout) swaps the title for an input; Enter or leaving the field
// saves, Escape cancels. A session is also named after its first message
// when it has no title yet, so one made with "New session" does not stay
// "Untitled".
const renamingID = ref<string | null>(null)
const renameDraft = ref('')
const renameInput = ref<HTMLElement | null>(null)

async function startRename(s: { id: string; title?: string }) {
  if (sending.value) return
  renamingID.value = s.id
  renameDraft.value = s.title ?? ''
  await nextTick()
  // In the page layout the input is inside v-for, where a template ref
  // would be an array, so it is found by its marker attribute instead.
  const el = (renameInput.value?.querySelector?.('input') as HTMLInputElement | null)
    ?? document.querySelector<HTMLInputElement>('input[data-rename-session]')
  el?.focus()
  el?.select()
}

function cancelRename() {
  renamingID.value = null
}

async function finishRename() {
  const id = renamingID.value
  if (!id) return
  renamingID.value = null
  const title = renameDraft.value.trim()
  const before = sessions.data.value.find(s => s.id === id)
  if (!before || (before.title ?? '') === title) return
  await rename(id, title)
}

async function rename(id: string, title: string): Promise<boolean> {
  loadingSession.value = true
  try {
    const sum = await props.api.sessions.rename(id, title)
    sessions.data.value = sessions.data.value.map(s => (s.id === id ? { ...s, title: sum.title } : s))
    if (current.value?.id === id) current.value = { ...current.value, title: sum.title }
    return true
  } catch (e) {
    emit('error', errorMessage(e))
    return false
  } finally {
    loadingSession.value = false
  }
}

async function select(id: string) {
  if (sending.value || !id) return
  loadingSession.value = true
  try {
    current.value = await props.api.sessions.get(id)
    live.value = []
    liveUsage.value = null
  } catch (e) {
    emit('error', errorMessage(e))
  } finally {
    loadingSession.value = false
  }
}

async function create(title?: string): Promise<AISession | null> {
  loadingSession.value = true
  try {
    const s = await props.api.sessions.create({
      title: title || undefined,
      credential: newSessionModel.value || undefined,
    })
    current.value = s
    live.value = []
    liveUsage.value = null
    sessions.run()
    return s
  } catch (e) {
    emit('error', errorMessage(e))
    return null
  } finally {
    loadingSession.value = false
  }
}

function toggle(key: string) {
  const set = new Set(expanded.value)
  if (set.has(key)) set.delete(key)
  else set.add(key)
  expanded.value = set
}

function appendDelta(kind: 'assistant' | 'thinking', text: string) {
  const last = live.value.at(-1)
  if (last && last.kind === kind) {
    last.text += text
    return
  }
  live.value.push({ kind, key: `l${++seq}`, text })
}

function onEvent(e: AIEvent) {
  switch (e.type) {
    case 'text':
      appendDelta('assistant', e.text ?? '')
      break
    case 'thinking':
      appendDelta('thinking', e.text ?? '')
      break
    case 'tool_use':
      if (e.call) live.value.push({ kind: 'tool', key: `c${e.call.id}`, call: e.call })
      break
    case 'tool_result': {
      const r = e.result
      if (!r) break
      const item = live.value.find(i => i.kind === 'tool' && i.call.id === r.id)
      if (item && item.kind === 'tool') item.result = r
      else live.value.push({ kind: 'tool', key: `r${r.id}`, call: { id: r.id, name: r.name, input: null }, result: r })
      break
    }
    case 'usage':
      if (e.usage) {
        const u = liveUsage.value ?? { input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 }
        u.input_tokens += e.usage.input_tokens
        u.output_tokens += e.usage.output_tokens
        u.cache_read_tokens += e.usage.cache_read_tokens
        u.cache_write_tokens += e.usage.cache_write_tokens
        liveUsage.value = u
      }
      break
    case 'compaction':
      live.value.push({ kind: 'note', key: `l${++seq}`, text: e.text ? `Context compacted: ${e.text}` : 'Earlier context was compacted.' })
      break
    case 'done':
      if (e.stop_reason && e.stop_reason !== 'end_turn') {
        live.value.push({ kind: 'note', key: `l${++seq}`, text: `Stopped: ${e.stop_reason}` })
      }
      break
    case 'error':
      live.value.push({ kind: 'note', key: `l${++seq}`, text: e.error || 'The run failed.', error: true })
      break
  }
}

async function send() {
  const content = draft.value.trim()
  if (!content || sending.value || !ready.value) return
  let s = current.value
  if (!s) {
    s = await create(content.slice(0, 60))
    if (!s) return
  } else if (!s.title?.trim()) {
    // Before the run starts, since a rename during a run is refused.
    await rename(s.id, content.slice(0, 60))
  }
  draft.value = ''
  sending.value = true
  controller = new AbortController()
  live.value.push({ kind: 'user', key: `l${++seq}`, text: content })
  try {
    await props.api.sendMessage(s.id, content, onEvent, controller.signal)
  } catch (e) {
    if ((e as Error)?.name === 'AbortError') {
      live.value.push({ kind: 'note', key: `l${++seq}`, text: 'Stopped.' })
    } else {
      const msg = errorMessage(e)
      live.value.push({ kind: 'note', key: `l${++seq}`, text: msg, error: true })
      emit('error', msg)
    }
  } finally {
    controller = null
  }
  // The stored session is authoritative for what the model saw and the
  // usage it cost; it replaces the live rows. The component stays busy
  // until it is loaded, so the assistant cannot be switched under a stale
  // reply.
  try {
    const fresh = await props.api.sessions.get(s.id)
    current.value = fresh
    live.value = live.value.filter(i => i.kind === 'note')
    liveUsage.value = null
  } catch (e) {
    emit('error', errorMessage(e))
  }
  await sessions.run()
  sending.value = false
}

function stop() {
  controller?.abort()
}

const pendingDelete = ref<AISessionSummary | null>(null)
const confirmOpen = ref(false)

function askDelete(s: AISessionSummary | null | undefined) {
  if (!s) return
  pendingDelete.value = s
  confirmOpen.value = true
}

async function doDelete() {
  if (!pendingDelete.value) return
  loadingSession.value = true
  try {
    await props.api.sessions.remove(pendingDelete.value.id)
    if (current.value?.id === pendingDelete.value.id) {
      current.value = null
      live.value = []
      liveUsage.value = null
    }
    await sessions.run()
  } finally {
    loadingSession.value = false
  }
}

function inputJSON(call: AIToolCall): string {
  if (call.input === null || call.input === undefined) return ''
  try {
    return JSON.stringify(call.input, null, 2)
  } catch {
    return String(call.input)
  }
}

// The model of the open session, or of the next one.
const modelLabel = computed(() => {
  const s = current.value
  if (s) return s.credential ? `${s.model || s.provider} (${s.credential})` : s.model || s.provider
  const m = models.value.find(x => x.name === newSessionModel.value) ?? status.data.value?.model
  return m ? `${m.model || m.provider} (${m.name})` : ''
})

const currentSummary = computed(() => sessions.data.value.find(s => s.id === current.value?.id) ?? null)
const placeholder = computed(() => {
  if (!ready.value) return 'Configure an AI model credential to start.'
  return props.serviceLabel ? `Message the ${props.serviceLabel} assistant. Enter sends, Shift+Enter adds a line.` : 'Message the assistant. Enter sends, Shift+Enter adds a line.'
})

defineExpose({ modelLabel, ready })
</script>

<template>
  <div class="flex h-full min-h-0 flex-col" data-slot="assistant-chat" :data-layout="layout">
    <div v-if="unconfigured" class="shrink-0" :class="layout === 'page' ? 'px-6 pb-4' : 'px-4 pb-3'">
      <AppNotice inline>
        <slot name="unconfigured">
          No AI model credential is configured for your account. Add a model credential on the Credentials page, or ask an administrator to add one for the whole account.
        </slot>
      </AppNotice>
    </div>
    <div v-else-if="status.error.value" class="shrink-0" :class="layout === 'page' ? 'px-6 pb-4' : 'px-4 pb-3'">
      <AppNotice inline tone="bad">{{ status.error.value }}</AppNotice>
    </div>

    <!-- Panel layout: the sessions are a dropdown above the transcript. -->
    <div v-if="layout === 'panel'" class="flex shrink-0 items-center gap-1 border-b px-3 py-2" data-slot="assistant-sessions-bar">
      <div v-if="renamingID && current && renamingID === current.id" ref="renameInput" class="min-w-0 flex-1">
        <Input
          v-model="renameDraft" data-rename-session class="h-7 text-sm" aria-label="Session title" placeholder="Session title"
          maxlength="200"
          @keydown.enter.prevent="finishRename" @keydown.escape.prevent="cancelRename" @blur="finishRename"
        />
      </div>
      <Select v-else :model-value="current?.id ?? ''" :disabled="busy" @update:model-value="v => select(String(v))">
        <SelectTrigger size="sm" class="min-w-0 flex-1" aria-label="Session">
          <SelectValue :placeholder="sessions.data.value.length ? 'Choose a session' : 'New conversation'" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem v-for="s in sessions.data.value" :key="s.id" :value="s.id">
            <span class="truncate">{{ titleOf(s) }}</span>
            <span class="ml-2 text-xs text-muted-foreground">{{ fmtRelative(s.updated_at) }}</span>
          </SelectItem>
        </SelectContent>
      </Select>
      <Button v-if="current" variant="ghost" size="icon-sm" aria-label="Rename session" :disabled="sending" @click="startRename(current)"><PencilIcon /></Button>
      <Button v-if="current" variant="ghost" size="icon-sm" aria-label="Delete session" :disabled="sending" @click="askDelete(currentSummary)"><Trash2Icon /></Button>
      <Button size="sm" variant="outline" :disabled="!ready || busy" aria-label="New session" @click="create()"><PlusIcon /> New</Button>
    </div>
    <div v-if="layout === 'panel' && models.length > 1" class="flex shrink-0 items-center gap-2 border-b px-3 py-1.5 text-xs text-muted-foreground">
      <span>Model</span>
      <Select :model-value="newSessionModel" :disabled="sending" @update:model-value="v => (chosenModel = String(v))">
        <SelectTrigger size="sm" class="min-w-0 flex-1" aria-label="Model for a new session"><SelectValue /></SelectTrigger>
        <SelectContent>
          <SelectItem v-for="m in models" :key="m.name" :value="m.name">{{ modelChoiceLabel(m) }}</SelectItem>
        </SelectContent>
      </Select>
    </div>

    <div class="flex min-h-0 flex-1" :class="layout === 'page' && 'border-t'">
      <!-- Page layout: the sessions are a sidebar. -->
      <aside v-if="layout === 'page'" class="flex w-64 shrink-0 flex-col border-r" data-slot="assistant-sessions">
        <div class="flex items-center gap-1 border-b px-2 py-2">
          <Select v-if="models.length > 1" :model-value="newSessionModel" :disabled="sending" @update:model-value="v => (chosenModel = String(v))">
            <SelectTrigger size="sm" class="min-w-0 flex-1" aria-label="Model for a new session"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem v-for="m in models" :key="m.name" :value="m.name">{{ modelChoiceLabel(m) }}</SelectItem>
            </SelectContent>
          </Select>
          <span v-else class="flex-1 px-1 text-xs text-muted-foreground">Sessions <span v-if="sessions.loading.value">(loading)</span></span>
          <Button size="sm" :disabled="!ready || busy" @click="create()"><PlusIcon /> New session</Button>
        </div>
        <div class="min-h-0 flex-1 overflow-auto">
          <p v-if="sessions.error.value" class="px-3 py-2 text-xs text-destructive">{{ sessions.error.value }}</p>
          <p v-else-if="!sessions.loading.value && sessions.data.value.length === 0" class="px-3 py-2 text-xs text-muted-foreground">No sessions yet.</p>
          <ul v-else>
            <li
              v-for="s in sessions.data.value"
              :key="s.id"
              class="group flex items-start gap-1 border-b px-3 py-2 text-sm hover:bg-accent"
              :class="current?.id === s.id ? 'bg-accent' : ''"
            >
              <div v-if="renamingID === s.id" class="min-w-0 flex-1 py-0.5">
                <Input
                  v-model="renameDraft" data-rename-session class="h-7 text-sm" aria-label="Session title" placeholder="Session title"
                  maxlength="200"
                  @keydown.enter.prevent="finishRename" @keydown.escape.prevent="cancelRename" @blur="finishRename"
                />
              </div>
              <button v-else type="button" class="min-w-0 flex-1 text-left" :disabled="sending" @click="select(s.id)" @dblclick="startRename(s)">
                <div class="truncate" :title="titleOf(s)">{{ titleOf(s) }}</div>
                <div class="truncate text-xs text-muted-foreground" :title="s.model || s.provider">{{ s.model || s.provider }}</div>
                <div class="text-xs text-muted-foreground">{{ fmtRelative(s.updated_at) }} <span v-if="s.message_count">&middot; {{ fmtInt(s.message_count) }} msgs</span></div>
              </button>
              <Button
                v-if="renamingID !== s.id"
                variant="ghost" size="icon-sm" class="opacity-0 group-hover:opacity-100" aria-label="Rename session"
                :disabled="sending" @click="startRename(s)"
              ><PencilIcon /></Button>
              <Button
                v-if="renamingID !== s.id"
                variant="ghost" size="icon-sm" class="opacity-0 group-hover:opacity-100" aria-label="Delete session"
                :disabled="sending" @click="askDelete(s)"
              ><Trash2Icon /></Button>
            </li>
          </ul>
        </div>
      </aside>

      <section class="flex min-w-0 flex-1 flex-col">
        <div ref="transcript" class="min-h-0 flex-1 overflow-auto" :class="layout === 'page' ? 'px-6 py-4' : 'px-3 py-3'" data-slot="assistant-transcript">
          <p v-if="loadingSession" class="text-sm text-muted-foreground">Loading session...</p>
          <p v-else-if="items.length === 0" class="text-sm text-muted-foreground">
            {{ current ? 'No messages yet.' : 'Start a conversation below.' }}
          </p>
          <div v-else class="mx-auto flex max-w-3xl flex-col gap-3">
            <template v-for="it in items" :key="it.key">
              <div v-if="it.kind === 'user'" class="flex justify-end">
                <div class="max-w-[85%] whitespace-pre-wrap rounded-lg bg-primary px-3 py-2 text-sm text-primary-foreground">{{ it.text }}</div>
              </div>
              <div v-else-if="it.kind === 'assistant'" class="flex justify-start">
                <div class="max-w-[85%] rounded-lg border bg-card px-3 py-2 text-sm" :class="hasChart(it.text) ? 'w-full' : ''">
                  <AssistantMarkdown :text="it.text" :streaming="isStreaming(it)" />
                </div>
              </div>
              <div v-else-if="it.kind === 'thinking'" class="text-xs text-muted-foreground">
                <button type="button" class="flex items-center gap-1" @click="toggle(it.key)">
                  <component :is="expanded.has(it.key) ? ChevronDownIcon : ChevronRightIcon" class="size-3" />
                  <span class="italic">Thinking</span>
                </button>
                <div v-if="expanded.has(it.key)" class="mt-1 whitespace-pre-wrap border-l pl-3 italic">{{ it.text }}</div>
              </div>
              <div v-else-if="it.kind === 'tool'" class="rounded-md border text-xs">
                <button type="button" class="flex w-full items-center gap-2 px-3 py-1.5 text-left hover:bg-accent" @click="toggle(it.key)">
                  <component :is="expanded.has(it.key) ? ChevronDownIcon : ChevronRightIcon" class="size-3 shrink-0" />
                  <WrenchIcon class="size-3 shrink-0 text-muted-foreground" />
                  <span class="truncate font-mono">Called {{ it.call.name }}</span>
                  <span v-if="!it.result" class="ml-auto shrink-0 text-muted-foreground">running</span>
                  <span v-else-if="it.result.denied" class="ml-auto shrink-0 text-destructive">denied</span>
                  <span v-else-if="it.result.result?.is_error" class="ml-auto shrink-0 text-destructive">error</span>
                </button>
                <div v-if="expanded.has(it.key)" class="grid gap-2 border-t px-3 py-2">
                  <div v-if="inputJSON(it.call)">
                    <div class="mb-1 text-muted-foreground">Input</div>
                    <pre class="max-h-64 overflow-auto whitespace-pre-wrap font-mono">{{ inputJSON(it.call) }}</pre>
                  </div>
                  <div v-if="it.result">
                    <div class="mb-1 text-muted-foreground">{{ it.result.denied ? 'Denied' : 'Result' }}</div>
                    <pre class="max-h-64 overflow-auto whitespace-pre-wrap font-mono" :class="it.result.denied || it.result.result?.is_error ? 'text-destructive' : ''">{{ it.result.result?.content }}</pre>
                  </div>
                </div>
              </div>
              <div v-else class="text-center text-xs" :class="it.error ? 'text-destructive' : 'text-muted-foreground'">{{ it.text }}</div>
            </template>
            <div v-if="sending && live.at(-1)?.kind !== 'assistant'" class="text-xs text-muted-foreground">Working...</div>
          </div>
        </div>

        <div class="shrink-0 border-t" :class="layout === 'page' ? 'px-6 py-3' : 'px-3 py-2'" data-slot="assistant-composer">
          <div class="mx-auto flex max-w-3xl flex-col gap-2">
            <div class="flex items-end gap-2">
              <Textarea
                v-model="draft"
                class="min-h-10 max-h-48 flex-1"
                :placeholder="placeholder"
                :disabled="!ready"
                @keydown.enter.exact.prevent="send"
              />
              <Button v-if="sending" variant="outline" size="sm" @click="stop"><SquareIcon /> Stop</Button>
              <Button v-else size="sm" :disabled="!ready || !draft.trim()" @click="send"><SendIcon /> Send</Button>
            </div>
            <div class="flex flex-wrap gap-x-3 text-xs tabular-nums text-muted-foreground">
              <span>{{ fmtInt(usage.input_tokens) }} in</span>
              <span>{{ fmtInt(usage.output_tokens) }} out</span>
              <span>{{ fmtInt(usage.cache_read_tokens) }} cache read</span>
              <span>{{ fmtInt(usage.cache_write_tokens) }} cache write</span>
              <span v-if="modelLabel" class="ml-auto truncate" :title="modelLabel">{{ modelLabel }}</span>
            </div>
          </div>
        </div>
      </section>
    </div>

    <ConfirmDialog
      v-model:open="confirmOpen"
      title="Delete session?"
      :description="`${pendingDelete ? titleOf(pendingDelete) : ''} and its messages will be removed.`"
      confirm-text="Delete"
      destructive
      :action="doDelete"
      @error="m => emit('error', m)"
    />
  </div>
</template>
