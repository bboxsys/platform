<script setup lang="ts">
import { EyeIcon, PencilIcon, PlusIcon, RefreshCwIcon, Trash2Icon } from '@lucide/vue'
import { Button } from '../ui/button'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '../ui/tooltip'

// The five things a table toolbar does, as icon buttons that look the same in
// every view: create is the one filled button, delete the one red mark, and
// refresh spins while it loads. The label is the tooltip, the title and the
// accessible name, so a view names the action once.
export type ObjectActionKind = 'create' | 'inspect' | 'edit' | 'delete' | 'refresh'

withDefaults(
  defineProps<{
    action: ObjectActionKind
    label?: string
    disabled?: boolean
    loading?: boolean
  }>(),
  { disabled: false, loading: false },
)
const icons = { create: PlusIcon, inspect: EyeIcon, edit: PencilIcon, delete: Trash2Icon, refresh: RefreshCwIcon }
const labels = {
  create: 'Create',
  inspect: 'Inspect selected',
  edit: 'Edit selected',
  delete: 'Delete selected',
  refresh: 'Refresh',
}
defineEmits<{ click: [event: MouseEvent] }>()
</script>

<template>
  <TooltipProvider>
    <Tooltip>
      <TooltipTrigger as-child>
        <!-- The span takes the hover so a disabled button still explains
             itself; a disabled button raises no pointer events of its own. -->
        <span class="inline-flex" :title="label || labels[action]">
          <Button
            type="button"
            :variant="action === 'create' ? 'default' : 'outline'"
            size="icon-sm"
            :data-action="action"
            :disabled="disabled || loading"
            :aria-busy="loading"
            :aria-label="label || labels[action]"
            @click="$emit('click', $event)"
          >
            <component
              :is="icons[action]"
              :class="{ 'text-destructive': action === 'delete', 'animate-spin': action === 'refresh' && loading }"
            />
          </Button>
        </span>
      </TooltipTrigger>
      <TooltipContent>{{ label || labels[action] }}</TooltipContent>
    </Tooltip>
  </TooltipProvider>
</template>
