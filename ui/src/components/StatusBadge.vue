<script setup lang="ts">
import { computed } from 'vue'
import { Badge } from '../ui/badge'

const props = defineProps<{ status: string }>()

const tone = computed(() => {
  switch (props.status) {
    case 'valid':
    case 'accepted':
    case 'active':
    case 'healthy':
    case 'complete':
      return 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400 border-emerald-500/30'
    case 'pending':
    case 'expiring':
    case 'warn':
    case 'waiting':
    case 'started':
      return 'bg-amber-500/15 text-amber-700 dark:text-amber-400 border-amber-500/30'
    case 'expired':
    case 'revoked':
    case 'locked':
    case 'error':
    case 'fatal':
    case 'cancelled':
      return 'bg-red-500/15 text-red-700 dark:text-red-400 border-red-500/30'
    default:
      return ''
  }
})
</script>

<template>
  <Badge variant="outline" :class="['capitalize', tone]">{{ status }}</Badge>
</template>
