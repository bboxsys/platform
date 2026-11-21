import { ref, shallowRef, type Ref } from 'vue'
import { HttpError } from '../lib/http'

export function errorMessage(e: unknown): string {
  if (e instanceof HttpError) {
    const j = e.json as { error?: string; message?: string } | undefined
    if (j?.error) return j.error
    if (j?.message) return j.message
    if (e.status === 401) return 'Not authorized. Sign in again.'
    if (e.status === 403) return 'You do not have permission to do that.'
    if (e.status === 404) return 'Not found.'
    return e.body.trim() || `Request failed (${e.status})`
  }
  return (e as Error)?.message ?? String(e)
}

// Wraps a fetcher with loading/error state. Call `run()` to (re)load; the
// latest call wins if runs overlap.
export function useAsync<T>(fetcher: () => Promise<T>, initial: T) {
  const data = shallowRef<T>(initial) as Ref<T>
  const loading = ref(false)
  const error = ref('')
  let gen = 0

  async function run(): Promise<T | undefined> {
    const g = ++gen
    loading.value = true
    error.value = ''
    try {
      const result = await fetcher()
      // A newer run() has started; this result is stale. Do not touch state
      // and do not hand the stale value back to this caller.
      if (g !== gen) return undefined
      data.value = result
      return result
    } catch (e) {
      if (g === gen) error.value = errorMessage(e)
      return undefined
    } finally {
      if (g === gen) loading.value = false
    }
  }

  return { data, loading, error, run }
}
