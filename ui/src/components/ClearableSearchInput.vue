<script setup lang="ts">
import type { HTMLAttributes } from 'vue'
import { nextTick, ref } from 'vue'
import { SearchIcon, XIcon } from '@lucide/vue'
import { cn } from '../lib/utils'
import { Input } from '../ui/input'

// The one search box: a leading search mark, and a clear button that appears
// once there is something to clear and hands focus back to the field. Every
// table filter and list search uses it, so a filter looks and clears the same
// way in every app.
defineOptions({ inheritAttrs: false })

const props = withDefaults(defineProps<{
  modelValue: string
  placeholder?: string
  ariaLabel?: string
  clearLabel?: string
  disabled?: boolean
  class?: HTMLAttributes['class']
  inputClass?: HTMLAttributes['class']
}>(), {
  placeholder: 'Search...',
  ariaLabel: 'Search',
  clearLabel: 'Clear search',
  disabled: false,
})

const emit = defineEmits<{
  'update:modelValue': [value: string]
}>()

const input = ref<{ $el?: HTMLInputElement } | null>(null)

async function clear() {
  emit('update:modelValue', '')
  await nextTick()
  input.value?.$el?.focus()
}
</script>

<template>
  <div :class="cn('relative', props.class)">
    <SearchIcon class="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
    <Input
      ref="input"
      :model-value="modelValue"
      :placeholder="placeholder"
      :aria-label="ariaLabel"
      :disabled="disabled"
      :class="cn('pl-8 pr-8', inputClass)"
      v-bind="$attrs"
      @update:model-value="value => emit('update:modelValue', String(value))"
    />
    <button
      v-if="modelValue"
      type="button"
      data-slot="search-clear"
      :aria-label="clearLabel"
      :title="clearLabel"
      :disabled="disabled"
      class="absolute right-1 top-1/2 inline-flex size-6 -translate-y-1/2 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50"
      @click="clear"
    >
      <XIcon class="size-3.5" />
    </button>
  </div>
</template>
