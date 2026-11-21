// The selection and deletion rules behind ObjectTable, kept out of the
// component so they can be tested without mounting one.

// What a row is called in a confirmation: the app's label, else its `name`,
// else its `key`, else the row key.
export function objectLabel<T>(row: T, rowKey: (row: T) => string, rowLabel?: (row: T) => string): string {
  const object = row as Record<string, unknown>
  return rowLabel?.(row)?.trim() || String(object.name ?? '').trim() || String(object.key ?? '').trim() || rowKey(row)
}

export function selectedVisibleRows<T>(rows: T[], keys: ReadonlySet<string>, rowKey: (row: T) => string): T[] {
  return rows.filter((row) => keys.has(rowKey(row)))
}

// A selection never outlives the rows on screen: a row filtered out or
// deleted elsewhere leaves the selection with it, so a bulk action cannot
// reach something the reader can no longer see.
export function retainedSelection<T>(rows: T[], keys: ReadonlySet<string>, rowKey: (row: T) => string): Set<string> {
  return new Set(selectedVisibleRows(rows, keys, rowKey).map(rowKey))
}

export interface RemoveFailure<T> {
  row: T
  message: string
}

// Deletes one row at a time and keeps going past a failure, so one refused
// row does not strand the rest. `canContinue` is asked before every row: once
// the table's scope has changed, what is left is reported as not deleted
// rather than deleted from a scope the reader has already left.
export async function removeObjects<T>(
  rows: readonly T[],
  remove: (row: T) => Promise<unknown>,
  canContinue: () => boolean = () => true,
): Promise<{ removed: T[]; failed: RemoveFailure<T>[] }> {
  const removed: T[] = []
  const failed: RemoveFailure<T>[] = []
  for (const row of rows) {
    if (!canContinue()) {
      failed.push({ row, message: 'Not deleted because the table scope changed.' })
      continue
    }
    try {
      await remove(row)
      removed.push(row)
    } catch (error) {
      failed.push({ row, message: error instanceof Error ? error.message : String(error) })
    }
  }
  return { removed, failed }
}
