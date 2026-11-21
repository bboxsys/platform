<script setup lang="ts">
import { computed } from 'vue'
import { ACCENT_SURFACE, type AccentTone } from '../theme/tone'

// A framed surface with a top rail and a wash at one accent. The accent is a
// token, so the same surface is blue in one app and orange in another.
const props = withDefaults(defineProps<{
  as?: string
  interactive?: boolean
  tone?: AccentTone
}>(), {
  as: 'section',
  interactive: false,
  tone: 'brand',
})

const treatment = computed(() => ACCENT_SURFACE[props.tone])
</script>

<template>
  <component
    :is="as"
    :class="[
      treatment.frame,
      interactive && 'transition-all duration-200 hover:-translate-y-0.5 hover:shadow-md',
    ]"
    class="relative overflow-hidden rounded-lg border bg-gradient-to-b via-card to-card shadow-sm"
  >
    <div :class="treatment.rail" class="absolute inset-x-0 top-0 h-1" />
    <slot />
  </component>
</template>
