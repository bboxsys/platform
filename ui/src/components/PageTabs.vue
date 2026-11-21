<script setup lang="ts">
import { computed, getCurrentInstance, ref } from 'vue'

// The standard tab bar: flat, left-aligned tabs on a bottom border, the
// active one underlined in the brand color. A tab either selects a value
// (v-model, for tabs inside one page) or links to a route (`to`, for tabs
// that are child routes); a bar may not mix the two.
export interface PageTab {
  label: string
  // Value tabs: what v-model holds when this tab is selected.
  value?: string
  // Route tabs: where the tab links. The active one is the link the router
  // marks aria-current="page".
  to?: string | Record<string, unknown>
  // A count shown after the label (hidden when zero or absent).
  count?: number
  disabled?: boolean
}

const props = defineProps<{
  tabs: PageTab[]
  modelValue?: string
  // Names the tab bar for assistive technology.
  label?: string
}>()

const emit = defineEmits<{ 'update:modelValue': [value: string] }>()

// The kit does not depend on vue-router. An app that installed it has
// RouterLink registered on the app; without one a route tab is a plain anchor.
const routerLink = getCurrentInstance()?.appContext.components.RouterLink

const routed = computed(() => props.tabs.some(t => t.to !== undefined))

const buttons = ref<HTMLButtonElement[]>([])

function select(t: PageTab) {
  if (t.disabled || t.value === undefined) return
  emit('update:modelValue', t.value)
}

// Arrow keys move between value tabs, as a tablist should.
function move(from: number, step: number) {
  const n = props.tabs.length
  for (let i = 1; i <= n; i++) {
    const j = (from + step * i + n) % n
    const t = props.tabs[j]
    if (!t.disabled && t.value !== undefined) {
      select(t)
      buttons.value[j]?.focus()
      return
    }
  }
}

const TAB = [
  'inline-flex items-center gap-1.5 whitespace-nowrap border-b-2 border-transparent px-1 py-2 text-sm text-muted-foreground transition-colors',
  'hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 rounded-t-sm',
  'aria-[current=page]:border-primary aria-[current=page]:font-medium aria-[current=page]:text-foreground',
  'aria-selected:border-primary aria-selected:font-medium aria-selected:text-foreground',
  'disabled:pointer-events-none disabled:opacity-50',
].join(' ')
</script>

<template>
  <div data-slot="page-tabs" class="flex items-end gap-4 border-b">
    <nav v-if="routed" class="-mb-px flex gap-4" :aria-label="props.label">
      <component
        :is="routerLink ?? 'a'"
        v-for="t in props.tabs"
        :key="typeof t.to === 'string' ? t.to : t.label"
        v-bind="routerLink ? { to: t.to } : { href: t.to }"
        :class="TAB"
      >
        {{ t.label }}
        <span v-if="t.count" class="rounded-full bg-muted px-1.5 text-xs tabular-nums text-muted-foreground">{{ t.count }}</span>
      </component>
    </nav>
    <div v-else role="tablist" class="-mb-px flex gap-4" :aria-label="props.label">
      <button
        v-for="(t, i) in props.tabs"
        :key="t.value ?? t.label"
        ref="buttons"
        type="button"
        role="tab"
        :aria-selected="t.value === props.modelValue"
        :tabindex="t.value === props.modelValue ? 0 : -1"
        :disabled="t.disabled"
        :class="TAB"
        @click="select(t)"
        @keydown.right.prevent="move(i, 1)"
        @keydown.left.prevent="move(i, -1)"
      >
        {{ t.label }}
        <span v-if="t.count" class="rounded-full bg-muted px-1.5 text-xs tabular-nums text-muted-foreground">{{ t.count }}</span>
      </button>
    </div>
    <div v-if="$slots.actions" class="ml-auto flex items-center gap-2 pb-1.5">
      <slot name="actions" />
    </div>
  </div>
</template>
