import { describe, it, expect } from 'vitest'
import { objectLabel, removeObjects, retainedSelection, selectedVisibleRows } from './objectTable'

interface Row { id: string; name?: string; key?: string }
const key = (r: Row) => r.id

describe('objectLabel', () => {
  it('prefers the app label, then name, then key, then the row key', () => {
    expect(objectLabel({ id: '1', name: 'web' }, key, () => 'web on prod')).toBe('web on prod')
    expect(objectLabel({ id: '1', name: 'web' }, key)).toBe('web')
    expect(objectLabel({ id: '1', key: 'k1' }, key)).toBe('k1')
    expect(objectLabel({ id: '1' }, key)).toBe('1')
  })

  it('falls through a blank label', () => {
    expect(objectLabel({ id: '1', name: 'web' }, key, () => '  ')).toBe('web')
  })
})

describe('selection helpers', () => {
  const rows: Row[] = [{ id: 'a' }, { id: 'b' }, { id: 'c' }]

  it('returns the selected rows in row order', () => {
    expect(selectedVisibleRows(rows, new Set(['c', 'a']), key).map(key)).toEqual(['a', 'c'])
  })

  it('drops keys whose rows are no longer listed', () => {
    expect([...retainedSelection(rows, new Set(['a', 'gone']), key)]).toEqual(['a'])
  })
})

describe('removeObjects', () => {
  const rows: Row[] = [{ id: 'a' }, { id: 'b' }, { id: 'c' }]

  it('keeps going past a failure and reports it', async () => {
    const result = await removeObjects(rows, async r => {
      if (r.id === 'b') throw new Error('in use')
    })
    expect(result.removed.map(key)).toEqual(['a', 'c'])
    expect(result.failed).toEqual([{ row: rows[1], message: 'in use' }])
  })

  it('stops deleting once the scope changes', async () => {
    let live = true
    const seen: string[] = []
    const result = await removeObjects(rows, async r => {
      seen.push(r.id)
      live = false
    }, () => live)
    expect(seen).toEqual(['a'])
    expect(result.removed.map(key)).toEqual(['a'])
    expect(result.failed.map(f => f.row.id)).toEqual(['b', 'c'])
  })
})
