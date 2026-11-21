import { computed, ref, type Ref } from 'vue'
import { errorMessage } from './useAsync'

// Server-side "load more" pagination for list and table views. It fetches
// fixed-size pages through a limit/offset fetcher plus a count fetcher and
// appends them as the user asks for more, so a view never downloads its whole
// dataset up front.
export function usePagedList<T>(
  fetchPage: (limit: number, offset: number) => Promise<T[]>,
  fetchCount: () => Promise<number>,
  pageSize = 200,
) {
  const rows = ref<T[]>([]) as Ref<T[]>
  const total = ref(0)
  const loading = ref(false)
  const error = ref('')
  const loaded = computed(() => rows.value.length)
  const hasMore = computed(() => rows.value.length < total.value)

  async function loadMore() {
    if (loading.value || (total.value > 0 && !hasMore.value)) return
    loading.value = true
    error.value = ''
    try {
      const page = await fetchPage(pageSize, rows.value.length)
      rows.value = [...rows.value, ...page]
    } catch (e) {
      error.value = errorMessage(e)
    } finally {
      loading.value = false
    }
  }

  async function reload() {
    loading.value = true
    error.value = ''
    rows.value = []
    try {
      total.value = await fetchCount()
      rows.value = await fetchPage(pageSize, 0)
    } catch (e) {
      error.value = errorMessage(e)
    } finally {
      loading.value = false
    }
  }

  return { rows, total, loading, error, loaded, hasMore, reload, loadMore }
}
