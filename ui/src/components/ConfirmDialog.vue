<script setup lang="ts">
import { ref } from 'vue'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter,
  AlertDialogHeader, AlertDialogTitle,
} from '../ui/alert-dialog'
import { buttonVariants } from '../ui/button'
import { errorMessage } from '../composables/useAsync'

const props = withDefaults(defineProps<{
  title: string
  description?: string
  confirmText?: string
  destructive?: boolean
  action: () => Promise<unknown>
}>(), { confirmText: 'Confirm', destructive: false })

const emit = defineEmits<{ done: []; error: [message: string] }>()
const open = defineModel<boolean>('open', { default: false })
const busy = ref(false)

async function confirm() {
  busy.value = true
  try {
    await props.action()
    open.value = false
    emit('done')
  } catch (e) {
    emit('error', errorMessage(e))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AlertDialog v-model:open="open">
    <AlertDialogContent>
      <AlertDialogHeader>
        <AlertDialogTitle>{{ title }}</AlertDialogTitle>
        <AlertDialogDescription v-if="description">{{ description }}</AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogFooter>
        <!-- Cancel stays enabled even while busy: the action() promise has no
             timeout, so disabling Cancel would wedge the dialog open forever if
             the request hangs. The confirm button is disabled to prevent a
             double submit. -->
        <AlertDialogCancel>Cancel</AlertDialogCancel>
        <AlertDialogAction
          :class="destructive ? buttonVariants({ variant: 'destructive' }) : ''"
          :disabled="busy"
          @click.prevent="confirm"
        >
          {{ busy ? 'Working' : confirmText }}
        </AlertDialogAction>
      </AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>
</template>
