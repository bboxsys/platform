<script setup lang="ts">
// Where page notices land. An app mounts this once, under its top toolbar and
// above the page, and every AppNotice a view raises teleports into it.
withDefaults(defineProps<{ id?: string }>(), { id: 'app-notices' })
</script>

<template>
  <div :id="id" class="shrink-0" />
</template>
