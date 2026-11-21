import { useDocumentVisibility, useIntervalFn } from '@vueuse/core'
import { watch } from 'vue'

// Periodic refresh that pauses while the tab is hidden, and refreshes once on
// the way back so the page is not a whole interval stale when it is looked at
// again. The interval stops with the component that started it.
export function usePolling(fn: () => void, intervalMs = 10000, immediate = true) {
  const visibility = useDocumentVisibility()
  const { isActive, pause, resume } = useIntervalFn(fn, intervalMs, { immediate })

  let pausedByVisibility = false
  watch(visibility, v => {
    if (v === 'hidden' && isActive.value) {
      pausedByVisibility = true
      pause()
    } else if (v === 'visible' && pausedByVisibility) {
      pausedByVisibility = false
      resume()
      fn()
    }
  })

  return { active: isActive, pause, resume }
}
