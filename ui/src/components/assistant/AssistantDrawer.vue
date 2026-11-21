<script setup lang="ts">
import { computed, ref } from 'vue'
import { BotIcon } from '@lucide/vue'
import type { AssistantService } from '../../types/admin'
import type { AssistantClient } from '../../lib/assistantApi'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '../../ui/sheet'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../../ui/select'
import AssistantChat from './AssistantChat.vue'

// The assistant as a right-side drawer over the current page, for an app
// that wants the conversation beside its views instead of on a page of
// its own: a toolbar button opens it, a picker in its header chooses
// which assistant answers (the host passes the list it got from
// client.services() and the default, normally the assistant of the module
// on screen), and the conversation is AssistantChat in its panel layout,
// keyed by the chosen assistant so a switch starts clean. The drawer holds
// no app knowledge: which assistants exist, which one the page belongs to
// and where credentials are managed all come from the host.
withDefaults(defineProps<{
  client: AssistantClient
  services: AssistantService[]
  title?: string
  description?: string
}>(), { title: 'Assistant', description: '' })

const emit = defineEmits<{ error: [message: string] }>()

const open = defineModel<boolean>('open', { default: false })
// The id of the assistant in use.
const service = defineModel<string>('service', { default: 'admin' })

const busy = ref(false)
// While a request is in flight the picker is locked: the chat is keyed by
// the service, so a switch would unmount it under its own reply.
const pickerDisabled = computed(() => busy.value)

// Closing the drawer unmounts the chat, which ends a run in progress the
// way leaving the console's page does: the browser disconnects, the
// backend cancels the run and keeps what it produced, and a reopen loads
// the session again.
</script>

<template>
  <Sheet :open="open" @update:open="v => (open = v)">
    <SheetContent side="right" class="flex w-full flex-col gap-0 p-0 data-[side=right]:sm:max-w-xl" data-slot="assistant-drawer">
      <SheetHeader class="shrink-0 gap-1 border-b px-4 py-3 pr-14">
        <SheetTitle class="flex items-center gap-2 text-sm">
          <BotIcon class="size-4" />
          {{ title }}
          <Select v-if="services.length > 1" :model-value="service" :disabled="pickerDisabled" @update:model-value="v => (service = String(v))">
            <SelectTrigger size="sm" class="ml-1 max-w-56" aria-label="Assistant"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem v-for="s in services" :key="s.id" :value="s.id">{{ s.name }}</SelectItem>
            </SelectContent>
          </Select>
        </SheetTitle>
        <SheetDescription v-if="description">{{ description }}</SheetDescription>
        <SheetDescription v-else class="sr-only">A conversation with the assistant.</SheetDescription>
      </SheetHeader>
      <div class="min-h-0 flex-1">
        <AssistantChat
          :key="service"
          v-model:busy="busy"
          :api="client.for(service)"
          layout="panel"
          :service-label="services.find(s => s.id === service)?.name ?? ''"
          @error="m => emit('error', m)"
        >
          <template v-if="$slots.unconfigured" #unconfigured><slot name="unconfigured" /></template>
        </AssistantChat>
      </div>
    </SheetContent>
  </Sheet>
</template>
