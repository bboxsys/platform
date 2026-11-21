<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { toast } from 'vue-sonner'
import { assistantClient } from '@/api/admin'
import { ADMIN_ASSISTANT, ALL_ASSISTANT, AssistantChat, PageHeader, errorMessage } from '@bboxsys/ui'
import type { AssistantService } from '@bboxsys/ui'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

// The Assistant page: the kit's conversation (AssistantChat, page layout)
// with a picker for which assistant answers. The admin's own is the
// default; a product that registered service assistants with the gateway
// adds one entry per service and, with them, "All", the coordinator that
// asks the others. The chat is keyed by the selection, so a switch starts
// clean and no reply can land in another assistant's store; the picker is
// disabled while anything is in flight.
const services = ref<AssistantService[]>([ADMIN_ASSISTANT])
const selectedService = ref(ADMIN_ASSISTANT.id)
const busy = ref(false)
const serviceLabel = computed(() => services.value.find(s => s.id === selectedService.value)?.name ?? ADMIN_ASSISTANT.name)

onMounted(async () => {
  try {
    services.value = await assistantClient.services()
  } catch (e) {
    toast.error(errorMessage(e))
  }
})

const description = computed(() => {
  if (selectedService.value === ALL_ASSISTANT.id) {
    return 'A conversation with the model configured for your account that answers by asking the other assistants, each with your permissions.'
  }
  return `A conversation with the model configured for your account, with the ${serviceLabel.value} API as its tools.`
})
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <PageHeader title="Assistant" :description="description">
      <Select v-if="services.length > 1" :model-value="selectedService" :disabled="busy" @update:model-value="v => (selectedService = String(v))">
        <SelectTrigger size="sm" class="w-44" aria-label="Assistant service"><SelectValue /></SelectTrigger>
        <SelectContent>
          <SelectItem v-for="s in services" :key="s.id" :value="s.id">{{ s.name }}</SelectItem>
        </SelectContent>
      </Select>
    </PageHeader>

    <AssistantChat
      :key="selectedService"
      v-model:busy="busy"
      :api="assistantClient.for(selectedService)"
      layout="page"
      :service-label="serviceLabel"
      class="min-h-0 flex-1"
      @error="m => toast.error(m)"
    >
      <template #unconfigured>
        No AI model credential is configured for your account.
        Add a <RouterLink :to="{ path: '/credentials', query: { tab: 'claude' } }" class="underline">Claude key</RouterLink>
        or an <RouterLink :to="{ path: '/credentials', query: { tab: 'models' } }" class="underline">Ollama or OpenAI-compatible model</RouterLink>
        on the Credentials page, or ask an administrator to add one for the whole account.
      </template>
    </AssistantChat>
  </div>
</template>
