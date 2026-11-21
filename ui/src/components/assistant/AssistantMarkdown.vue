<script lang="ts">
// The assistant's text is Markdown from the model, with charts as fenced
// code blocks tagged echarts (see ai.ChartGuidance). Splitting is done on
// marked's token stream: every top-level token that is not a chart block
// is rendered to HTML in order, and a chart block becomes an AssistantChart.
// The HTML passes through DOMPurify because model output is untrusted:
// raw HTML in the text, scripts, event handlers, and javascript: URLs are
// stripped before v-html sees them. marked and DOMPurify are loaded on
// first use, so an app that never opens the assistant does not ship them.
// Exported for the unit tests.
import type { Token, TokensList } from 'marked'

export type Segment =
  | { kind: 'html'; key: number; html: string }
  | { kind: 'chart'; key: number; source: string; closed: boolean }

export interface MarkdownEngine {
  marked: typeof import('marked').marked
  purify: typeof import('dompurify').default
}

const CHART_LANG = 'echarts'

let enginePromise: Promise<MarkdownEngine> | null = null

// Loads marked and DOMPurify once. Links open in a new tab and never carry
// the opener; set after sanitizing so the attributes cannot be forged by
// the text itself.
export function loadMarkdownEngine(): Promise<MarkdownEngine> {
  if (!enginePromise) {
    enginePromise = Promise.all([import('marked'), import('dompurify')]).then(([m, d]) => {
      const purify = d.default
      purify.addHook('afterSanitizeAttributes', node => {
        if (node.tagName === 'A') {
          node.setAttribute('target', '_blank')
          node.setAttribute('rel', 'noopener noreferrer')
        }
      })
      return { marked: m.marked, purify }
    })
  }
  return enginePromise
}

export function renderMarkdown(engine: MarkdownEngine, tokens: Token[], links: TokensList['links']): string {
  const list = Object.assign([] as Token[], { links }) as TokensList
  list.push(...tokens)
  const html = engine.marked.parser(list, { gfm: true, breaks: false, async: false }) as string
  return engine.purify.sanitize(html, { USE_PROFILES: { html: true } })
}

export function splitSegments(engine: MarkdownEngine, text: string): Segment[] {
  const tokens = engine.marked.lexer(text, { gfm: true })
  const out: Segment[] = []
  let pending: Token[] = []
  let key = 0
  const flush = () => {
    if (pending.length === 0) return
    out.push({ kind: 'html', key: key++, html: renderMarkdown(engine, pending, tokens.links) })
    pending = []
  }
  for (const t of tokens) {
    if (t.type === 'code' && (t.lang ?? '').trim().toLowerCase() === CHART_LANG) {
      flush()
      // An unterminated fence (text still streaming in) lexes as a code
      // token whose raw text has no closing fence line.
      const closed = /\n[ \t]*(`{3,}|~{3,})[ \t]*\n?$/.test(t.raw)
      out.push({ kind: 'chart', key: key++, source: t.text, closed })
      continue
    }
    pending.push(t)
  }
  flush()
  return out
}
</script>

<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import AssistantChart from './AssistantChart.vue'

const props = defineProps<{
  text: string
  // The text is still arriving: an unfinished chart block shows a
  // placeholder instead of a parse error.
  streaming?: boolean
}>()

// The rendered segments; until the engine has loaded (the first render
// only) the raw text is shown so nothing flashes empty.
const segments = ref<Segment[] | null>(null)
let engine: MarkdownEngine | null = null
let gone = false

function render(text: string) {
  if (!engine) return
  segments.value = splitSegments(engine, text)
}

watch(() => props.text, text => {
  if (engine) {
    render(text)
    return
  }
  loadMarkdownEngine().then(e => {
    if (gone) return
    engine = e
    render(props.text)
  }).catch(() => { segments.value = null })
}, { immediate: true })

onBeforeUnmount(() => { gone = true })
</script>

<template>
  <div class="assistant-markdown" data-slot="assistant-markdown">
    <div v-if="segments === null" class="whitespace-pre-wrap">{{ text }}</div>
    <template v-for="seg in segments ?? []" v-else :key="seg.key">
      <div v-if="seg.kind === 'html'" class="prose" v-html="seg.html" />
      <div v-else-if="!seg.closed && streaming" class="my-2 rounded-md border border-dashed px-3 py-6 text-center text-xs text-muted-foreground">Drawing chart...</div>
      <AssistantChart v-else :source="seg.source" />
    </template>
  </div>
</template>

<style scoped>
/* Markdown from the model, styled against the shared tokens. Tailwind's
   preflight resets every element, so the prose rules restore the few
   things a reply needs: spacing, lists, code, tables, and quotes. */
.prose :deep(> :first-child) { margin-top: 0; }
.prose :deep(> :last-child) { margin-bottom: 0; }
.prose :deep(p) { margin: 0.5rem 0; line-height: 1.55; }
.prose :deep(h1), .prose :deep(h2), .prose :deep(h3), .prose :deep(h4) {
  margin: 0.9rem 0 0.4rem;
  font-weight: 600;
  line-height: 1.3;
}
.prose :deep(h1) { font-size: 1.15em; }
.prose :deep(h2) { font-size: 1.08em; }
.prose :deep(h3), .prose :deep(h4) { font-size: 1em; }
.prose :deep(ul), .prose :deep(ol) { margin: 0.4rem 0; padding-left: 1.4rem; }
.prose :deep(ul) { list-style: disc; }
.prose :deep(ol) { list-style: decimal; }
.prose :deep(li) { margin: 0.15rem 0; }
.prose :deep(li > p) { margin: 0.15rem 0; }
.prose :deep(a) { color: var(--primary); text-decoration: underline; text-underline-offset: 2px; }
.prose :deep(strong) { font-weight: 600; }
.prose :deep(code) {
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 0.875em;
  background: var(--muted);
  border-radius: 4px;
  padding: 0.1em 0.35em;
}
.prose :deep(pre) {
  margin: 0.6rem 0;
  padding: 0.6rem 0.75rem;
  background: var(--muted);
  border-radius: 6px;
  overflow-x: auto;
  font-size: 0.8rem;
  line-height: 1.45;
}
.prose :deep(pre code) { background: none; padding: 0; font-size: inherit; }
.prose :deep(blockquote) {
  margin: 0.6rem 0;
  padding-left: 0.75rem;
  border-left: 3px solid var(--border);
  color: var(--muted-foreground);
}
.prose :deep(hr) { margin: 0.8rem 0; border: 0; border-top: 1px solid var(--border); }
.prose :deep(table) { margin: 0.6rem 0; border-collapse: collapse; font-size: 0.85em; width: 100%; }
.prose :deep(th), .prose :deep(td) { padding: 0.3rem 0.6rem; border: 1px solid var(--border); text-align: left; vertical-align: top; }
.prose :deep(th) { background: var(--muted); font-weight: 600; }
.prose :deep(td.right), .prose :deep(th[align='right']), .prose :deep(td[align='right']) { text-align: right; font-variant-numeric: tabular-nums; }
.prose :deep(img) { max-width: 100%; border-radius: 6px; }
</style>
