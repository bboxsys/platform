import { afterEach, beforeEach, describe, it, expect, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import DataTable, { type Column } from './DataTable.vue'

interface Row {
  id: string
  name: string
  size: number
}

const rows: Row[] = [
  { id: '1', name: 'Charlie', size: 30 },
  { id: '2', name: 'Alice', size: 10 },
  { id: '3', name: 'Bob', size: 20 },
]

const columns: Column<Row>[] = [
  { key: 'name', label: 'Name', sortable: true },
  { key: 'size', label: 'Size', sortable: true },
]

function mountTable(props: Record<string, unknown> = {}, slots: Record<string, unknown> = {}) {
  return mount(DataTable as any, {
    props: {
      columns,
      rows,
      rowKey: (r: Row) => r.id,
      ...props,
    },
    slots,
  })
}

const many = (n: number, from = 0) => Array.from({ length: n }, (_, i) => ({ id: String(from + i), name: `row ${from + i}`, size: from + i }))

function bodyRowTexts(wrapper: ReturnType<typeof mountTable>): string[] {
  return wrapper.findAll('tbody tr').map(r => r.text())
}

describe('DataTable', () => {
  it('renders one row per data item', () => {
    const wrapper = mountTable()
    const texts = bodyRowTexts(wrapper)
    expect(texts).toHaveLength(3)
    expect(texts.join(' ')).toContain('Charlie')
  })

  it('narrows visible rows when filtering by the search box', async () => {
    const wrapper = mountTable()
    await wrapper.find('input').setValue('ali')
    const texts = bodyRowTexts(wrapper)
    expect(texts).toHaveLength(1)
    expect(texts[0]).toContain('Alice')
  })

  it('shows a "no rows match" message when the filter excludes everything', async () => {
    const wrapper = mountTable()
    await wrapper.find('input').setValue('zzzzz')
    expect(wrapper.text()).toContain('No rows match the filter.')
  })

  it('sorts ascending, then descending, then clears on repeated header clicks', async () => {
    const wrapper = mountTable()
    const nameHeader = wrapper.findAll('thead th')[0]

    // First click -> ascending by name: Alice, Bob, Charlie
    await nameHeader.trigger('click')
    expect(bodyRowTexts(wrapper).map(t => t.replace(/[0-9]/g, ''))).toEqual(['Alice', 'Bob', 'Charlie'])

    // Second click -> descending: Charlie, Bob, Alice
    await nameHeader.trigger('click')
    expect(bodyRowTexts(wrapper).map(t => t.replace(/[0-9]/g, ''))).toEqual(['Charlie', 'Bob', 'Alice'])

    // Third click -> unsorted, back to original insertion order
    await nameHeader.trigger('click')
    expect(bodyRowTexts(wrapper).map(t => t.replace(/[0-9]/g, ''))).toEqual(['Charlie', 'Alice', 'Bob'])
  })

  it('emits rowClick when clickable is true', async () => {
    const wrapper = mountTable({ clickable: true })
    await wrapper.findAll('tbody tr')[0].trigger('click')
    expect(wrapper.emitted('rowClick')).toBeTruthy()
    expect(wrapper.emitted('rowClick')![0][0]).toMatchObject({ name: 'Charlie' })
  })

  it('does NOT emit rowClick when clickable is false (regression)', async () => {
    const wrapper = mountTable({ clickable: false })
    await wrapper.findAll('tbody tr')[0].trigger('click')
    expect(wrapper.emitted('rowClick')).toBeFalsy()
  })

  describe('row expansion', () => {
    const expanded = { expanded: '<div class="detail">detail for {{ params.row.name }}</div>' }

    it('adds no caret column without an expanded slot', () => {
      const wrapper = mountTable()
      expect(wrapper.findAll('thead th')).toHaveLength(2)
      expect(wrapper.find('[data-slot="expand-toggle"]').exists()).toBe(false)
    })

    it('shows the slot content beneath a row once its caret is clicked, and hides it again', async () => {
      const wrapper = mountTable({}, expanded)
      expect(wrapper.findAll('thead th')).toHaveLength(3)
      expect(wrapper.find('.detail').exists()).toBe(false)

      const toggle = wrapper.findAll('[data-slot="expand-toggle"]')[0]
      await toggle.trigger('click')
      const rowsAfter = wrapper.findAll('tbody tr')
      expect(rowsAfter).toHaveLength(4)
      expect(rowsAfter[1].attributes('data-slot')).toBe('expanded-row')
      expect(rowsAfter[1].text()).toContain('detail for Charlie')
      expect(wrapper.emitted('expand')![0]).toEqual([rows[0], true])

      await wrapper.findAll('[data-slot="expand-toggle"]')[0].trigger('click')
      expect(wrapper.find('.detail').exists()).toBe(false)
      expect(wrapper.emitted('expand')![1]).toEqual([rows[0], false])
    })

    it('renders a caret only for rows the expandable predicate accepts', () => {
      const wrapper = mountTable({ expandable: (r: Row) => r.size > 15 }, expanded)
      expect(wrapper.findAll('[data-slot="expand-toggle"]')).toHaveLength(2)
      const cells = wrapper.findAll('tbody tr').map(r => r.findAll('td').length)
      expect(cells).toEqual([3, 3, 3])
    })

    it('does not fire rowClick when the caret is clicked', async () => {
      const wrapper = mountTable({ clickable: true }, expanded)
      await wrapper.findAll('[data-slot="expand-toggle"]')[0].trigger('click')
      expect(wrapper.emitted('rowClick')).toBeFalsy()
      expect(wrapper.find('.detail').exists()).toBe(true)
    })

    it('forgets the expansion of a row that disappears from rows', async () => {
      const wrapper = mountTable({}, expanded)
      await wrapper.findAll('[data-slot="expand-toggle"]')[0].trigger('click')
      expect(wrapper.find('.detail').exists()).toBe(true)

      await wrapper.setProps({ rows: rows.slice(1) })
      expect(wrapper.find('.detail').exists()).toBe(false)

      // The row coming back does not reopen it.
      await wrapper.setProps({ rows })
      expect(wrapper.find('.detail').exists()).toBe(false)
    })

    it('uses expandTitle for the caret tooltip', async () => {
      const wrapper = mountTable({ expandTitle: (r: Row, open: boolean) => (open ? 'Hide ' : 'Show ') + r.name }, expanded)
      const toggle = wrapper.findAll('[data-slot="expand-toggle"]')[0]
      expect(toggle.attributes('title')).toBe('Show Charlie')
      await toggle.trigger('click')
      expect(wrapper.findAll('[data-slot="expand-toggle"]')[0].attributes('title')).toBe('Hide Charlie')
    })
  })
})


describe('DataTable sticky header', () => {
  it('keeps the header in view by default, scrolling rows inside a bounded container', async () => {
    const w = mountTable()
    await flushPromises()
    const container = w.find('[data-slot="table-container"]')
    expect(container.attributes('data-sticky-header')).toBe('true')
    // The default height is the measured available space, a pixel value.
    expect(container.attributes('style')).toMatch(/max-height: \d+px/)
    expect(container.classes()).toContain('overflow-auto')
    expect(w.find('thead').classes()).toEqual(expect.arrayContaining(['sticky', 'top-0']))
    // No second scroll container between the sticky header and the bounded one.
    expect(w.findAll('[data-slot="table-container"]')).toHaveLength(1)
  })

  it('takes a custom height and can be turned off', () => {
    expect(mountTable({ maxHeight: '24rem' }).find('[data-slot="table-container"]').attributes('style')).toContain('max-height: 24rem')
    const off = mountTable({ stickyHeader: false })
    expect(off.find('[data-slot="table-container"]').attributes('data-sticky-header')).toBeUndefined()
    expect(off.find('[data-slot="table-container"]').attributes('style')).toBeUndefined()
    expect(off.find('thead').classes()).not.toContain('sticky')
  })

  it('lets a view replace a header cell through head-{key}', () => {
    const w = mountTable({}, { 'head-size': '<em class="custom">Bytes</em>' })
    expect(w.find('thead .custom').text()).toBe('Bytes')
  })
})

describe('DataTable available height', () => {
  // happy-dom has no layout: the table's position and the viewport are
  // stubbed, resize observers are collected and fired by hand, and frames
  // run on the fake clock.
  let sizes: ResizeObserverCallback[]
  const rect = (top: number, bottom: number) =>
    ({ top, bottom, left: 0, right: 1000, width: 1000, height: bottom - top, x: 0, y: top, toJSON() {} }) as DOMRect
  async function flush() {
    await flushPromises()
    await vi.advanceTimersByTimeAsync(20)
    await flushPromises()
  }
  const container = (w: ReturnType<typeof mountTable>) => w.find('[data-slot="table-container"]').element as HTMLElement

  beforeEach(() => {
    vi.useFakeTimers()
    sizes = []
    vi.stubGlobal('ResizeObserver', class {
      constructor(callback: ResizeObserverCallback) { sizes.push(callback) }
      observe() {}
      disconnect() {}
    })
    vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => setTimeout(() => callback(0), 1))
    vi.stubGlobal('cancelAnimationFrame', (id: number) => clearTimeout(id))
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue(rect(100, 1000))
    Object.defineProperty(window, 'innerHeight', { configurable: true, value: 1000, writable: true })
  })
  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('fits the remaining viewport and updates after window and container resizing', async () => {
    const w = mountTable({ rows: many(250), searchable: false })
    await flush()
    expect(container(w).style.maxHeight).toBe('884px')
    window.innerHeight = 700
    window.dispatchEvent(new Event('resize'))
    await flush()
    expect(container(w).style.maxHeight).toBe('584px')
    vi.mocked(HTMLElement.prototype.getBoundingClientRect).mockReturnValue(rect(160, 700))
    sizes.at(-1)!([], {} as ResizeObserver)
    await flush()
    expect(container(w).style.maxHeight).toBe('524px')
    w.unmount()
  })

  it('never measures below the minimum height', async () => {
    vi.mocked(HTMLElement.prototype.getBoundingClientRect).mockReturnValue(rect(950, 1000))
    const w = mountTable({ searchable: false })
    await flush()
    expect(container(w).style.maxHeight).toBe('160px')
    w.unmount()
  })

  it('retains explicit height limits for embedded tables', async () => {
    const w = mountTable({ maxHeight: '18rem', searchable: false })
    await flush()
    window.innerHeight = 700
    window.dispatchEvent(new Event('resize'))
    await flush()
    expect(container(w).style.maxHeight).toBe('18rem')
    expect(sizes).toHaveLength(0)
    w.unmount()
  })

  it('keeps table height stable as its surrounding page scrolls', async () => {
    const host = document.createElement('div')
    document.body.append(host)
    const w = mount(DataTable as any, {
      props: { columns, rows: many(250), rowKey: (r: Row) => r.id, searchable: false },
      attachTo: host,
    })
    await flush()
    const original = container(w).style.maxHeight
    Object.defineProperty(host, 'scrollTop', { configurable: true, value: 200 })
    vi.mocked(HTMLElement.prototype.getBoundingClientRect).mockReturnValue(rect(-100, 800))
    host.dispatchEvent(new Event('scroll', { bubbles: true }))
    await flush()
    expect(container(w).style.maxHeight).toBe(original)
    sizes.at(-1)!([], {} as ResizeObserver)
    await flush()
    expect(container(w).style.maxHeight).toBe(original)
    w.unmount()
    host.remove()
  })

  it('keeps the header inside the scroll container across batches', async () => {
    const w = mountTable({ rows: many(250), searchable: false })
    await flush()
    const box = w.find('[data-sticky-header="true"]')
    expect(box.exists()).toBe(true)
    expect(box.element.contains(w.find('thead').element)).toBe(true)
    expect(w.find('thead').classes()).toContain('sticky')
    w.unmount()
  })
})

describe('DataTable keyboard activation', () => {
  it('activates a clickable row with Enter and Space and suppresses the key default', async () => {
    const w = mountTable({ clickable: true })
    const row = w.findAll('tbody tr')[0]
    expect(row.attributes('tabindex')).toBe('0')
    await row.trigger('keydown', { key: 'Enter' })
    const space = new KeyboardEvent('keydown', { key: ' ', bubbles: true, cancelable: true })
    row.element.dispatchEvent(space)
    await flushPromises()
    expect(space.defaultPrevented).toBe(true)
    expect(w.emitted('rowClick')).toHaveLength(2)
    expect(w.emitted('rowClick')![0][0]).toMatchObject({ name: 'Charlie' })
  })

  it('leaves non-clickable rows out of the tab order and inert to keys', async () => {
    const w = mountTable()
    const row = w.findAll('tbody tr')[0]
    expect(row.attributes('tabindex')).toBeUndefined()
    const space = new KeyboardEvent('keydown', { key: ' ', bubbles: true, cancelable: true })
    row.element.dispatchEvent(space)
    await flushPromises()
    expect(space.defaultPrevented).toBe(false)
    expect(w.emitted('rowClick')).toBeFalsy()
  })

  it('does not activate the row from a key pressed in a nested control', async () => {
    const w = mountTable({ clickable: true }, { actions: '<button class="nested">Go</button>' })
    const button = w.find('button.nested')
    const space = new KeyboardEvent('keydown', { key: ' ', bubbles: true, cancelable: true })
    button.element.dispatchEvent(space)
    await flushPromises()
    expect(space.defaultPrevented).toBe(false)
    expect(w.emitted('rowClick')).toBeFalsy()
  })
})

describe('DataTable infinite scroll', () => {
  // jsdom has no layout, so stand in for the observer and fire it by hand.
  const observers: Array<{ cb: IntersectionObserverCallback; root: Element | null }> = []
  class FakeObserver {
    cb: IntersectionObserverCallback
    root: Element | null
    constructor(cb: IntersectionObserverCallback, opts?: IntersectionObserverInit) {
      this.cb = cb
      this.root = (opts?.root as Element) ?? null
      observers.push(this)
    }
    observe() {}
    disconnect() {}
  }
  const reachEnd = async () => {
    const o = observers[observers.length - 1]
    o.cb([{ isIntersecting: true } as IntersectionObserverEntry], o as unknown as IntersectionObserver)
    await flushPromises()
  }
  beforeEach(() => {
    observers.length = 0
    ;(globalThis as unknown as { IntersectionObserver: unknown }).IntersectionObserver = FakeObserver
  })
  afterEach(() => {
    delete (globalThis as unknown as { IntersectionObserver?: unknown }).IntersectionObserver
  })

  it('draws rows a batch at a time as the end comes into view', async () => {
    const w = mountTable({ rows: many(250), batchSize: 100 })
    await flushPromises()
    expect(w.findAll('tbody tr:not([data-slot="load-more"])')).toHaveLength(100)
    expect(observers.at(-1)!.root).toBe(w.find('[data-slot="table-container"]').element)
    await reachEnd()
    expect(w.findAll('tbody tr:not([data-slot="load-more"])')).toHaveLength(200)
    await reachEnd()
    expect(w.findAll('tbody tr:not([data-slot="load-more"])')).toHaveLength(250)
    expect(w.find('[data-slot="load-more"]').exists()).toBe(false)
  })

  it('asks the view for more once every loaded row is drawn, once per load', async () => {
    const w = mountTable({ rows: many(30), batchSize: 100, hasMore: true })
    await flushPromises()
    expect(w.find('[data-slot="load-more"]').exists()).toBe(true)
    await reachEnd()
    await reachEnd()
    expect(w.emitted('loadMore')).toHaveLength(1)
    await w.setProps({ rows: many(60) })
    await flushPromises()
    await reachEnd()
    expect(w.emitted('loadMore')).toHaveLength(2)
    await w.setProps({ hasMore: false })
    await flushPromises()
    expect(w.find('[data-slot="load-more"]').exists()).toBe(false)
  })

  it('does not ask for more while loading', async () => {
    const w = mountTable({ rows: many(10), hasMore: true, loading: true })
    await flushPromises()
    await reachEnd()
    expect(w.emitted('loadMore')).toBeUndefined()
    expect(w.find('[data-slot="load-more"]').text()).toBe('Loading more...')
  })

  it('starts over from the first batch when the sort changes', async () => {
    const w = mountTable({ rows: many(250), batchSize: 100 })
    await flushPromises()
    await reachEnd()
    expect(w.findAll('tbody tr:not([data-slot="load-more"])')).toHaveLength(200)
    await w.find('thead th').trigger('click')
    await flushPromises()
    expect(w.findAll('tbody tr:not([data-slot="load-more"])')).toHaveLength(100)
  })

  it('draws everything with batchSize 0', async () => {
    const w = mountTable({ rows: many(250), batchSize: 0 })
    await flushPromises()
    expect(w.findAll('tbody tr')).toHaveLength(250)
  })
})
