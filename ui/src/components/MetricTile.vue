<script setup lang="ts">
// The one metric tile: a label, a headline number, and either a caption or a
// meter. Views that hand-roll this markup drift in padding, type scale and
// border treatment; this is that markup, once.
//
// The left rail groups a tile by what it reports on -- see RAIL in
// theme/tone.ts -- and a status tone takes over whenever the tile carries a
// real reading.
import type { Component } from 'vue'
import { computed, getCurrentInstance } from 'vue'
import { ChevronRightIcon } from '@lucide/vue'
import { RAIL, RAIL_ICON, RAIL_METER, TONE_DOT, type Rail, type Tone } from '../theme/tone'
import { Progress } from '../ui/progress'

const props = withDefaults(defineProps<{
  label:    string
  value?:   string
  caption?: string
  icon?:    Component
  rail?:    Rail
  // 0-100. Renders a meter in place of the caption, colored by the rail.
  meter?:   number
  // Renders a status dot beside the value, for a tile whose reading is a
  // state rather than a number.
  dot?:     Tone
  // A route makes the tile a link; otherwise `clickable` makes it a button.
  to?:      string
  clickable?: boolean
}>(), { rail: 'brand' })

defineEmits<{ click: [] }>()

// The kit does not depend on vue-router. An app that installed it has
// RouterLink registered on the app, and the tile uses that; an app without a
// router gets a plain anchor.
const routerLink = getCurrentInstance()?.appContext.components.RouterLink
const tag = computed(() => (props.to ? routerLink ?? 'a' : 'div'))
const linkAttrs = computed(() => {
  if (!props.to) return {}
  return routerLink ? { to: props.to } : { href: props.to }
})
</script>

<template>
  <component
    :is="tag"
    v-bind="linkAttrs"
    data-slot="metric-tile"
    :role="props.clickable && !props.to ? 'button' : undefined"
    :tabindex="props.clickable && !props.to ? 0 : undefined"
    class="block rounded-md border border-l-2 p-3"
    :class="[
      RAIL[props.rail],
      (props.to || props.clickable) && 'cursor-pointer transition-colors hover:border-brand/50 hover:bg-brand/[0.08]',
    ]"
    @click="props.clickable && $emit('click')"
    @keydown.enter="props.clickable && $emit('click')"
    @keydown.space.prevent="props.clickable && $emit('click')"
  >
    <div class="flex items-center justify-between gap-2 text-xs text-muted-foreground">
      <span class="truncate">{{ props.label }}</span>
      <component :is="props.icon" v-if="props.icon" class="size-3.5 shrink-0" :class="RAIL_ICON[props.rail]" />
      <ChevronRightIcon v-else-if="props.clickable" class="size-3.5 shrink-0 opacity-40" />
    </div>

    <div v-if="props.value" class="mt-1 flex items-center gap-2">
      <span v-if="props.dot" class="size-2.5 shrink-0 rounded-full" :class="TONE_DOT[props.dot]" />
      <span class="truncate text-2xl font-semibold">{{ props.value }}</span>
    </div>

    <Progress v-if="props.meter !== undefined" :model-value="props.meter" class="mt-2" :indicator-class="RAIL_METER[props.rail]" />
    <div v-else-if="props.caption" class="truncate text-[11px] text-muted-foreground">{{ props.caption }}</div>
  </component>
</template>
