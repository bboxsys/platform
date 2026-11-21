import { describe, it, expect, vi } from 'vitest'
import { usePagedList } from './usePagedList'

const all = Array.from({ length: 5 }, (_, i) => i)
const fetchPage = (limit: number, offset: number) => Promise.resolve(all.slice(offset, offset + limit))
const fetchCount = () => Promise.resolve(all.length)

describe('usePagedList', () => {
  it('loads the first page and the total on reload', async () => {
    const list = usePagedList(fetchPage, fetchCount, 2)
    await list.reload()
    expect(list.rows.value).toEqual([0, 1])
    expect(list.total.value).toBe(5)
    expect(list.hasMore.value).toBe(true)
  })

  it('appends pages until everything is loaded, then stops fetching', async () => {
    const page = vi.fn(fetchPage)
    const list = usePagedList(page, fetchCount, 2)
    await list.reload()
    await list.loadMore()
    await list.loadMore()
    expect(list.rows.value).toEqual(all)
    expect(list.hasMore.value).toBe(false)
    const calls = page.mock.calls.length
    await list.loadMore()
    expect(page.mock.calls.length).toBe(calls)
  })

  it('reports a failed fetch as an error message', async () => {
    const list = usePagedList(() => Promise.reject(new Error('offline')), fetchCount, 2)
    await list.reload()
    expect(list.error.value).toBe('offline')
    expect(list.loading.value).toBe(false)
  })
})
