<script setup lang="ts">
import type { Component } from 'vue'
import { computed } from 'vue'
import { AlertTriangleIcon, CircleCheckIcon, InfoIcon } from '@lucide/vue'
import { TONE_NOTICE, type Tone } from '../theme/tone'

// A page-level notice for an OPERATIONAL condition -- an unreachable service,
// a stale inventory, a credential worth warning about. Something is wrong with
// the data on screen or the reach behind it, and the reader has to act.
//
// Not for explanation. What a thing is, how a list is derived, why a column
// reads the way it does: that belongs in help, one click away, instead of
// taking a band off every page.
//
// A view that draws its own banner pushes the page down by a different amount
// on every screen and puts the warning in a different place each time. This
// teleports into the app's AppNoticeBand instead, so notices always appear in
// one spot, above the page rather than inside it, and they leave with the view
// that raised them. `inline` renders the notice where it is written, for a
// surface that has no band above it (a dialog, a login page).
const { tone = 'warn', icon, to = '#app-notices', inline = false } = defineProps<{
  tone?: Tone
  // Overrides the tone's default mark where a more specific one reads better.
  icon?: Component
  to?: string
  inline?: boolean
}>()

const TONE_ICON: Record<Tone, Component> = {
  default: InfoIcon,
  muted:   InfoIcon,
  good:    CircleCheckIcon,
  warn:    AlertTriangleIcon,
  bad:     AlertTriangleIcon,
}

const mark = computed(() => icon ?? TONE_ICON[tone])
</script>

<template>
  <Teleport :to="to" defer :disabled="inline">
    <div
      data-slot="app-notice"
      class="flex gap-2 border-b px-4 py-2 text-xs"
      :class="[TONE_NOTICE[tone], inline && 'rounded-md border']"
    >
      <component :is="mark" class="mt-0.5 size-3.5 shrink-0" />
      <!-- A fleet can fail in as many ways as it has members, so a long list
           scrolls inside the band rather than taking the page. -->
      <div class="max-h-24 min-w-0 flex-1 space-y-1 overflow-auto"><slot /></div>
    </div>
  </Teleport>
</template>
