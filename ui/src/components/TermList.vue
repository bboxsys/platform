<script setup lang="ts">
import { computed } from 'vue'
import { Badge } from '../ui/badge'

// A capped list of short terms for a table cell. An uncapped cell of long
// names makes every row several lines tall and the table stops being
// scannable. The overflow count carries the full list in its title; a row
// expansion or an inspector is where the list appears in full.
const { terms, limit = 4, empty = '--', mono = false } = defineProps<{
  terms:  string[]
  limit?: number
  empty?: string
  mono?:  boolean
}>()

const shown  = computed(() => terms.slice(0, limit))
const hidden = computed(() => terms.slice(limit))

// A cell that needs more than a badge per term (a link, a tone) replaces
// the term or the overflow badge through these slots; the defaults stay.
defineSlots<{
  term?: (props: { term: string }) => unknown
  overflow?: (props: { terms: string[] }) => unknown
}>()
</script>

<template>
  <span class="flex flex-wrap items-center gap-1">
    <template v-for="term in shown" :key="term">
      <slot name="term" :term="term">
        <Badge
          variant="outline"
          :class="['max-w-64 truncate text-[10px]', mono && 'font-mono']"
          :title="term"
        >{{ term }}</Badge>
      </slot>
    </template>
    <slot v-if="hidden.length" name="overflow" :terms="hidden">
      <Badge
        variant="secondary"
        class="text-[10px] tabular-nums"
        data-slot="term-overflow"
        :title="hidden.join(', ')"
      >+{{ hidden.length }}</Badge>
    </slot>
    <span v-if="!terms.length" class="text-xs text-muted-foreground">{{ empty }}</span>
  </span>
</template>
