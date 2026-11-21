<script setup lang="ts">
// The header's light/dark switch. One component rather than one per app, so
// the label, icon pairing and button size cannot drift.
import { MoonIcon, SunIcon } from '@lucide/vue'
import { Button } from '../ui/button'
import { useTheme } from '../theme/useTheme'

const { isDark, toggle } = useTheme()
</script>

<template>
  <Button variant="ghost" size="icon-sm" aria-label="Toggle theme" @click="toggle()">
    <SunIcon v-if="isDark" />
    <MoonIcon v-else />
  </Button>
</template>
