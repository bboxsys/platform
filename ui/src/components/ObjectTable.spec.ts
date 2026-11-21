import { afterEach, beforeEach, describe, it, expect, vi } from 'vitest'
import { h } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import ObjectTable from './ObjectTable.vue'
import type { Column } from './DataTable.vue'

interface Row {
  id: string
  name: string
  zone: string
  locked?: boolean
  note?: string
}

const rows: Row[] = [
  { id: '1', name: 'web', zone: 'prod' },
  { id: '2', name: 'web', zone: 'test' },
  { id: '3', name: 'db', zone: 'prod', locked: true, note: 'primary replica' },
]

const columns: Column<Row>[] = [
  { key: 'name', label: 'Name', sortable: true },
  { key: 'zone', label: 'Zone' },
]

function mountTable(props: Record<string, unknown> = {}, slots: Record<string, unknown> = {}) {
  return mount(ObjectTable as any, {
    props: { columns, rows, rowKey: (r: Row) => r.id, ...props },
    slots,
    attachTo: document.body,
  })
}

type Wrapper = ReturnType<typeof mountTable>

const bodyRows = (w: Wrapper) => w.findAll('tbody tr')
const count = (w: Wrapper) => w.find('[data-slot="row-count"]').text().replace(/\s+/g, ' ')
const action = (w: Wrapper, name: string) => w.find(`button[data-action="${name}"]`)
const rowBoxes = (w: Wrapper) => w.findAll('[data-slot="select-row"]')

// The delete confirmation is a dialog, which renders in a portal on the body.
const dialog = () => document.body.querySelector('[role="dialog"]') as HTMLElement | null
async function typeConfirmation(text: string) {
  const input = dialog()!.querySelector('input')!
  input.value = text
  input.dispatchEvent(new Event('input', { bubbles: true }))
  await flushPromises()
}
async function pressConfirm() {
  ;(dialog()!.querySelector('[data-slot="confirm-delete"]') as HTMLButtonElement).click()
  await flushPromises()
}

describe('ObjectTable', () => {
  it('renders the rows under a toolbar with a count', () => {
    const w = mountTable()
    expect(w.find('[role="toolbar"]').exists()).toBe(true)
    expect(bodyRows(w)).toHaveLength(3)
    expect(count(w)).toBe('3')
    w.unmount()
  })

  it('matches every term across columns', async () => {
    const w = mountTable()
    await w.find('input[aria-label="Filter objects"]').setValue('prod web')
    expect(bodyRows(w)).toHaveLength(1)
    expect(bodyRows(w)[0].text()).toContain('prod')
    expect(count(w)).toBe('1 of 3')
    w.unmount()
  })

  it('searches text the columns do not show', async () => {
    const w = mountTable({ rowSearchText: (r: Row) => r.note ?? '' })
    await w.find('input[aria-label="Filter objects"]').setValue('replica')
    expect(bodyRows(w)).toHaveLength(1)
    expect(bodyRows(w)[0].text()).toContain('db')
    w.unmount()
  })

  it('matches out-of-order characters only when fuzzy', async () => {
    const exact = mountTable()
    await exact.find('input[aria-label="Filter objects"]').setValue('pd')
    expect(exact.text()).toContain('No objects match the filter.')
    exact.unmount()

    const fuzzy = mountTable({ fuzzySearch: true })
    await fuzzy.find('input[aria-label="Filter objects"]').setValue('pd')
    expect(bodyRows(fuzzy)).toHaveLength(2)
    fuzzy.unmount()
  })

  it('names rows by the noun it is given', async () => {
    const w = mountTable({ noun: 'route' })
    await w.find('input[aria-label="Filter routes"]').setValue('zzz')
    expect(w.text()).toContain('No routes match the filter.')
    w.unmount()
  })

  it('selects rows and reports the selection', async () => {
    const w = mountTable()
    await rowBoxes(w)[0].trigger('click')
    expect(count(w)).toBe('1 selected · 3')
    expect((w.emitted('selectionChange')!.at(-1)![0] as Row[]).map(r => r.id)).toEqual(['1'])
    await w.find('[data-slot="select-all"]').trigger('click')
    expect(count(w)).toBe('3 selected · 3')
    w.unmount()
  })

  it('drops rows from the selection when the filter hides them', async () => {
    const w = mountTable()
    await w.find('[data-slot="select-all"]').trigger('click')
    await w.find('input[aria-label="Filter objects"]').setValue('db')
    expect(count(w)).toBe('1 selected · 1 of 3')
    w.unmount()
  })

  it('has no selection column or selection actions when not selectable', () => {
    const w = mountTable({ selectable: false, clickable: true })
    expect(rowBoxes(w)).toHaveLength(0)
    expect(w.find('[data-slot="select-all"]').exists()).toBe(false)
    expect(action(w, 'inspect').exists()).toBe(false)
    w.unmount()
  })

  it('clears the selection when the scope changes', async () => {
    const w = mountTable({ selectionScope: 'account-a' })
    await rowBoxes(w)[0].trigger('click')
    expect(count(w)).toBe('1 selected · 3')
    await w.setProps({ selectionScope: 'account-b' })
    expect(count(w)).toBe('3')
    w.unmount()
  })

  it('inspects and edits the one selected row', async () => {
    const edit = vi.fn()
    const w = mountTable({ clickable: true, edit })
    expect(action(w, 'inspect').attributes('disabled')).toBeDefined()
    expect(action(w, 'edit').attributes('disabled')).toBeDefined()
    await rowBoxes(w)[1].trigger('click')
    await action(w, 'inspect').trigger('click')
    expect((w.emitted('rowClick')!.at(-1)![0] as Row).id).toBe('2')
    await action(w, 'edit').trigger('click')
    expect(edit).toHaveBeenCalledWith(rows[1])
    w.unmount()
  })

  it('keeps the row click but drops the inspect action when asked', async () => {
    const w = mountTable({ clickable: true, inspect: false })
    expect(action(w, 'inspect').exists()).toBe(false)
    await bodyRows(w)[0].trigger('click')
    expect(w.emitted('rowClick')).toHaveLength(1)
    w.unmount()
  })

  it('does not treat a checkbox click as a row click', async () => {
    const w = mountTable({ clickable: true })
    await rowBoxes(w)[0].trigger('click')
    expect(w.emitted('rowClick')).toBeUndefined()
    await bodyRows(w)[0].trigger('click')
    expect(w.emitted('rowClick')).toHaveLength(1)
    w.unmount()
  })

  it('refuses to delete a selection that holds a protected row', async () => {
    const w = mountTable({ remove: vi.fn(), canRemove: (r: Row) => !r.locked })
    await w.find('[data-slot="select-all"]').trigger('click')
    expect(action(w, 'delete').attributes('disabled')).toBeDefined()
    w.unmount()
  })

  it('deletes one row after its name is typed', async () => {
    const remove = vi.fn().mockResolvedValue(undefined)
    const w = mountTable({ remove, noun: 'route', rowLabel: (r: Row) => `${r.name} in ${r.zone}` })
    await rowBoxes(w)[0].trigger('click')
    await action(w, 'delete').trigger('click')
    await flushPromises()
    expect(dialog()!.textContent).toContain('Delete 1 route?')

    await typeConfirmation('wrong')
    await pressConfirm()
    expect(remove).not.toHaveBeenCalled()

    await typeConfirmation('web in prod')
    await pressConfirm()
    expect(remove).toHaveBeenCalledWith(rows[0])
    expect((w.emitted('removed')!.at(-1)![0] as Row[]).map(r => r.id)).toEqual(['1'])
    expect(dialog()).toBeNull()
    w.unmount()
  })

  it('confirms a bulk delete by count and keeps the failures open', async () => {
    const remove = vi.fn(async (r: Row) => {
      if (r.id === '2') throw new Error('in use')
    })
    const w = mountTable({ remove })
    await w.find('[data-slot="select-all"]').trigger('click')
    await action(w, 'delete').trigger('click')
    await flushPromises()
    expect(dialog()!.textContent).toContain('Delete 3 objects?')

    await typeConfirmation('DELETE 3')
    await pressConfirm()
    expect(remove).toHaveBeenCalledTimes(3)
    expect((w.emitted('removed')!.at(-1)![0] as Row[]).map(r => r.id)).toEqual(['1', '3'])
    expect(dialog()!.textContent).toContain('in use')
    expect(dialog()!.textContent).toContain('Delete 1 object?')
    expect(dialog()!.textContent).toContain('Retry failed deletions')
    w.unmount()
  })

  it('forwards cell, toolbar and expansion slots', async () => {
    const w = mountTable({}, {
      'cell-name': ({ row }: { row: Row }) => h('strong', `name:${row.name}`),
      'toolbar-actions': ({ selected }: { selected: Row[] }) => h('span', { id: 'picked' }, `picked:${selected.length}`),
      'filters': () => h('span', { id: 'filters' }, 'zone filter'),
      'expanded': ({ row }: { row: Row }) => h('p', `detail:${row.id}`),
    })
    expect(w.find('tbody strong').text()).toBe('name:web')
    expect(w.find('#filters').text()).toBe('zone filter')
    expect(w.find('#picked').text()).toBe('picked:0')
    await rowBoxes(w)[0].trigger('click')
    expect(w.find('#picked').text()).toBe('picked:1')
    await w.find('[data-slot="expand-toggle"]').trigger('click')
    expect(w.find('[data-slot="expanded-row"]').text()).toBe('detail:1')
    w.unmount()
  })

  it('frames the table in an accent when given one', () => {
    const plain = mountTable()
    expect(plain.find('[data-slot="object-table"]').classes()).not.toContain('border')
    plain.unmount()
    const framed = mountTable({ accent: 'highlight' })
    expect(framed.find('[data-slot="object-table"]').classes()).toContain('border-brand-highlight/30')
    framed.unmount()
  })
})

describe('ObjectTable select-all in the header', () => {
  it('puts select-all in the selection column header, not the toolbar', () => {
    const w = mountTable()
    expect(w.find('thead [data-slot="select-all"]').exists()).toBe(true)
    expect(w.find('[role="toolbar"] [data-slot="select-all"]').exists()).toBe(false)
    expect(w.find('thead').classes()).toContain('sticky')
    w.unmount()
  })

  it('forwards a view header slot without replacing select-all', () => {
    const w = mountTable({}, { 'head-zone': '<span class="custom-head">Region</span>' })
    expect(w.find('thead .custom-head').text()).toBe('Region')
    expect(w.find('thead [data-slot="select-all"]').exists()).toBe(true)
    w.unmount()
  })
})

describe('ObjectTable infinite scroll', () => {
  const observers: Array<{ cb: IntersectionObserverCallback; target?: Element }> = []
  class FakeObserver {
    cb: IntersectionObserverCallback
    target?: Element
    constructor(cb: IntersectionObserverCallback) { this.cb = cb; observers.push(this) }
    observe(target: Element) { this.target = target }
    disconnect() { this.target = undefined }
  }
  const reachEnd = async () => {
    const o = [...observers].reverse().find(item => item.target)!
    o.cb([{ isIntersecting: true } as IntersectionObserverEntry], o as unknown as IntersectionObserver)
    await flushPromises()
  }
  const many = (n: number) => Array.from({ length: n }, (_, i) => ({ id: String(i), name: `Resource ${i}`, zone: 'prod' }))
  const dataRows = (w: Wrapper) => w.findAll('tbody tr:not([data-slot="load-more"])')

  beforeEach(() => {
    observers.length = 0
    ;(globalThis as unknown as { IntersectionObserver: unknown }).IntersectionObserver = FakeObserver
  })
  afterEach(() => {
    delete (globalThis as unknown as { IntersectionObserver?: unknown }).IntersectionObserver
  })

  it('draws rows in batches without limiting the count or the selection', async () => {
    const all = many(250)
    const w = mountTable({ rows: all, batchSize: 50 })
    await flushPromises()
    expect(dataRows(w)).toHaveLength(50)
    expect(count(w)).toBe('250')
    expect(w.find('[data-slot="pager"]').exists()).toBe(false)
    await w.find('[data-slot="select-all"]').trigger('click')
    expect((w.emitted('selectionChange')!.at(-1)![0] as Row[])).toHaveLength(250)
    expect(count(w)).toBe('250 selected · 250')
    await reachEnd()
    expect(dataRows(w)).toHaveLength(100)
    w.unmount()
  })

  it('reads pageSize as a batch size for older callers', async () => {
    const w = mountTable({ rows: many(30), pageSize: 10 })
    await flushPromises()
    expect(dataRows(w)).toHaveLength(10)
    expect(count(w)).toBe('30')
    w.unmount()
  })

  it('forwards loadMore for a server-paged list', async () => {
    const w = mountTable({ rows: many(20), batchSize: 100, hasMore: true })
    await flushPromises()
    expect(w.find('[data-slot="load-more"]').exists()).toBe(true)
    await reachEnd()
    await reachEnd()
    expect(w.emitted('loadMore')).toHaveLength(1)
    await w.setProps({ rows: many(40) })
    await flushPromises()
    await reachEnd()
    expect(w.emitted('loadMore')).toHaveLength(2)
    w.unmount()
  })
})
