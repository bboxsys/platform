<script setup lang="ts" generic="T">
import { computed, onBeforeUnmount, ref, shallowRef, useSlots, watch } from 'vue'
import DataTable, { type Column } from './DataTable.vue'
import ClearableSearchInput from './ClearableSearchInput.vue'
import ObjectAction from './ObjectAction.vue'
import { objectLabel, removeObjects, retainedSelection, selectedVisibleRows, type RemoveFailure } from '../lib/objectTable'
import { ACCENT_SURFACE, type AccentTone } from '../theme/tone'
import { Button } from '../ui/button'
import { Checkbox } from '../ui/checkbox'
import { Input } from '../ui/input'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../ui/dialog'

// A DataTable under the standard toolbar. The toolbar reads the same in every
// view of every app: the filter on the left (select-all heads the selection
// column of the table itself), then the view's
// own filters; the count, the actions on the selection (inspect, edit,
// delete) and the view's own actions (refresh, create) on the right.
//
// Rows are acted on through the SELECTION rather than through a button per
// row, so one row and forty rows are deleted the same way, and a delete is
// confirmed by typing what is being deleted.
//
// The component knows nothing about routing, sessions or toasts. An app says
// when the selection stops being meaningful through `selectionScope` (the
// route, the account, the credential -- anything whose change puts different
// objects behind the same keys) and reports a deletion to its reader from the
// `removed` event.
const props = withDefaults(
  defineProps<{
    columns: Column<T>[]
    rows: T[]
    rowKey: (row: T) => string
    rowLabel?: (row: T) => string
    rowDescription?: (row: T) => string
    // Text to match beyond the columns, for fields a row carries but the
    // table does not show.
    rowSearchText?: (row: T) => string
    fuzzySearch?: boolean
    // Infinite scroll, as DataTable: rows are drawn batchSize at a time and
    // loadMore is forwarded for a server-paged list. Counts, the filter and
    // the selection always cover every supplied row, drawn or not.
    batchSize?: number
    hasMore?: boolean
    // Deprecated since 0.18.0: the Previous / Next pager is gone. pageSize
    // is read as a fallback batch size so old callers keep scrolling.
    pageSize?: number
    loading?: boolean
    error?: string
    emptyText?: string
    searchable?: boolean
    searchPlaceholder?: string
    clickable?: boolean
    // Whether `clickable` also puts the inspect action in the toolbar. A
    // table whose row click does something other than open the row (a file
    // browser entering a folder) turns it off.
    inspect?: boolean
    rowClass?: string | ((row: T) => string)
    expandable?: (row: T) => boolean
    expandTitle?: (row: T, expanded: boolean) => string
    selectable?: boolean
    selectionScope?: string
    edit?: (row: T) => unknown
    remove?: (row: T) => Promise<unknown>
    canEdit?: (row: T) => boolean
    canRemove?: (row: T) => boolean
    deleteDescription?: string
    // What a row is called in the toolbar and the delete confirmation.
    noun?: string
    nounPlural?: string
    accent?: AccentTone
    // Keep the header row (and the select-all box in it) in view while the
    // rows scroll inside the table; see DataTable.
    stickyHeader?: boolean
    maxHeight?: string
  }>(),
  {
    searchable: true,
    selectable: true,
    inspect: true,
    loading: false,
    searchPlaceholder: 'Filter...',
    deleteDescription: 'This action cannot be undone.',
    noun: 'object',
    stickyHeader: true,
    maxHeight: 'available',
  },
)
const emit = defineEmits<{
  loadMore: []
  rowClick: [row: T]
  expand: [row: T, expanded: boolean]
  selectionChange: [rows: T[]]
  removed: [rows: T[]]
}>()
defineSlots<{
  [name: `cell-${string}`]: (props: { row: T; value?: string | number }) => unknown
  [name: `head-${string}`]: (props: { column: Column<T> }) => unknown
  actions?: (props: { row: T }) => unknown
  expanded?: (props: { row: T }) => unknown
  filters?: () => unknown
  toolbar?: () => unknown
  'toolbar-actions'?: (props: { selected: T[]; busy: boolean }) => unknown
  'selection-actions'?: (props: { row: T }) => unknown
}>()
const slots = useSlots()
const search = ref('')
const selection = ref(new Set<string>())
const pending = shallowRef<T[]>([])
const failures = shallowRef<RemoveFailure<T>[]>([])
const deleting = ref(false)
const confirmation = ref('')
let pendingRemove: ((row: T) => Promise<unknown>) | undefined
let scopeVersion = 0

const plural = computed(() => props.nounPlural ?? `${props.noun}s`)
const counted = (count: number) => (count === 1 ? props.noun : plural.value)

const label = (row: T) => objectLabel(row, props.rowKey, props.rowLabel)
const description = (row: T) =>
  props.rowDescription?.(row) ?? (props.rowKey(row) === label(row) ? '' : props.rowKey(row))
const classesFor = (row: T) =>
  [
    typeof props.rowClass === 'function' ? props.rowClass(row) : props.rowClass,
    selection.value.has(props.rowKey(row)) ? 'bg-muted/50' : '',
  ]
    .filter(Boolean)
    .join(' ')

function matchesSearchTerm(value: string, term: string): boolean {
  if (value.includes(term)) return true
  if (!props.fuzzySearch) return false
  let termIndex = 0
  for (const character of value) {
    if (character === term[termIndex]) termIndex++
    if (termIndex === term.length) return true
  }
  return false
}

// Every term has to match somewhere in the row, in any column, so "prod web"
// finds the row whose cluster is prod and whose name is web.
const visible = computed(() => {
  const terms = search.value.trim().toLowerCase().split(/\s+/).filter(Boolean)
  return !terms.length
    ? props.rows
    : props.rows.filter((row) => {
        const columns = props.columns
          .map(column => column.value ? column.value(row) : (row as Record<string, unknown>)[column.key])
          .filter(value => value !== null && value !== undefined)
          .map(value => String(value).toLowerCase())
        const extra = props.rowSearchText?.(row).toLowerCase() ?? ''
        return terms.every(term => columns.some(value => matchesSearchTerm(value, term)) || extra.includes(term))
      })
})
const selected = computed(() => selectedVisibleRows(visible.value, selection.value, props.rowKey))
const allSelected = computed(() => visible.value.length > 0 && selected.value.length === visible.value.length)
const single = computed(() => (selected.value.length === 1 ? selected.value[0] : undefined))
const canDelete = computed(
  () => !!selected.value.length && selected.value.every((row) => !props.canRemove || props.canRemove(row)),
)
const columns = computed<Column<T>[]>(() => [
  ...(props.selectable ? [{ key: '__selection', label: '', class: 'w-10' }] : []),
  ...props.columns.filter((column) => column.key !== 'actions' || !slots['selection-actions']),
])
const forwardedSlots = computed(
  () =>
    Object.keys(slots).filter(
      (name) => !name.startsWith('head-') && !['toolbar', 'toolbar-actions', 'selection-actions', 'filters', 'cell-__selection'].includes(name),
    ) as Array<`cell-${string}` | 'actions' | 'expanded'>,
)
// Header slots a view passes for its own columns; the selection column's
// header is this component's select-all.
const forwardedHeadSlots = computed(
  () => Object.keys(slots).filter((name) => name.startsWith('head-') && name !== 'head-__selection') as Array<`head-${string}`>,
)
const accentClasses = computed(() => props.accent ? ACCENT_SURFACE[props.accent] : undefined)
const confirmText = computed(() =>
  pending.value.length === 1 ? label(pending.value[0]!) : `DELETE ${pending.value.length}`,
)
watch(visible, (rows) => {
  selection.value = retainedSelection(rows, selection.value, props.rowKey)
})
watch(selected, (rows) => emit('selectionChange', rows))

// A change of scope puts different objects behind the table, possibly under
// the same keys. The selection is dropped at once -- sync, so nothing acts on
// it in between -- and a deletion already in flight stops before its next row.
watch(
  () => props.selectionScope,
  () => {
    scopeVersion++
    selection.value = new Set()
    if (!deleting.value) {
      pending.value = []
      failures.value = []
    }
  },
  { flush: 'sync' },
)
onBeforeUnmount(() => {
  scopeVersion++
})

function toggle(row: T, checked: boolean | 'indeterminate') {
  const next = new Set(selection.value)
  if (checked === true) next.add(props.rowKey(row))
  else next.delete(props.rowKey(row))
  selection.value = next
}
function toggleAll(checked: boolean | 'indeterminate') {
  selection.value = checked === true ? new Set(visible.value.map(props.rowKey)) : new Set()
}
function askDelete() {
  if (!props.remove || !canDelete.value) return
  pending.value = [...selected.value]
  pendingRemove = props.remove
  failures.value = []
  confirmation.value = ''
}
async function confirmDelete() {
  if (!pendingRemove || deleting.value || confirmation.value !== confirmText.value) return
  deleting.value = true
  const version = scopeVersion
  try {
    const result = await removeObjects([...pending.value], pendingRemove, () => version === scopeVersion)
    if (version !== scopeVersion) {
      pending.value = []
      failures.value = []
      return
    }
    const next = new Set(selection.value)
    for (const row of result.removed) next.delete(props.rowKey(row))
    selection.value = next
    failures.value = result.failed
    pending.value = result.failed.map((failure) => failure.row)
    confirmation.value = ''
    if (result.removed.length) emit('removed', result.removed)
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <div
    :class="accentClasses && [accentClasses.frame, 'relative overflow-hidden rounded-lg border bg-gradient-to-b via-card to-card p-4 shadow-sm']"
    class="min-w-0 space-y-3"
    data-slot="object-table"
  >
    <div v-if="accentClasses" :class="accentClasses.rail" class="absolute inset-x-0 top-0 h-1" />
    <div class="flex flex-wrap items-center gap-2" role="toolbar" :aria-label="`${noun} table controls`">
      <ClearableSearchInput
        v-if="searchable"
        v-model="search"
        :placeholder="searchPlaceholder"
        :aria-label="`Filter ${plural}`"
        clear-label="Clear filter"
        class="w-64 max-w-full"
        :disabled="deleting"
      />
      <div v-if="$slots.filters || $slots.toolbar" class="flex flex-wrap items-center gap-2">
        <slot name="filters" />
        <slot name="toolbar" />
      </div>
      <div class="ml-auto flex flex-wrap items-center justify-end gap-2">
        <span class="text-xs tabular-nums text-muted-foreground" role="status" data-slot="row-count">
          {{ selected.length ? `${selected.length} selected · ` : '' }}{{ visible.length }}
          <template v-if="visible.length !== rows.length">of {{ rows.length }}</template>
        </span>
        <ObjectAction
          v-if="clickable && selectable && inspect"
          action="inspect"
          :label="`Inspect selected ${noun}`"
          :disabled="!single || loading || deleting"
          @click="single && emit('rowClick', single)"
        />
        <ObjectAction
          v-if="edit"
          action="edit"
          :label="`Edit selected ${noun}`"
          :disabled="!single || loading || deleting || (!!canEdit && !canEdit(single!))"
          @click="single && edit(single)"
        />
        <ObjectAction
          v-if="remove"
          action="delete"
          :label="`Delete selected ${plural}`"
          :disabled="!canDelete || loading || deleting"
          @click="askDelete"
        />
        <fieldset
          v-if="single && $slots['selection-actions']"
          :disabled="loading || deleting"
          class="flex items-center gap-2"
        >
          <slot name="selection-actions" :row="single" />
        </fieldset>
        <fieldset :disabled="deleting" class="flex items-center gap-2">
          <slot name="toolbar-actions" :selected="selected" :busy="deleting" />
        </fieldset>
      </div>
    </div>
    <DataTable
      :columns="columns"
      :rows="visible"
      :batch-size="batchSize ?? pageSize ?? 100"
      :has-more="hasMore"
      :row-key="rowKey"
      :loading="loading"
      :error="error"
      :empty-text="search ? `No ${plural} match the filter.` : emptyText"
      :searchable="false"
      :clickable="clickable && !deleting"
      :row-class="classesFor"
      :expandable="expandable"
      :expand-title="expandTitle"
      :sticky-header="stickyHeader"
      :max-height="maxHeight"
      @row-click="!deleting && emit('rowClick', $event)"
      @expand="(row, expanded) => emit('expand', row, expanded)"
      @load-more="emit('loadMore')"
    >
      <!-- Select-all heads the selection column, so it stays in view with
           the sticky header and lines up with the row checkboxes. -->
      <template #head-__selection>
        <Checkbox
          data-slot="select-all"
          :model-value="allSelected ? true : selected.length ? 'indeterminate' : false"
          :disabled="loading || deleting || !visible.length"
          :aria-label="`Select all filtered ${plural}`"
          @update:model-value="toggleAll"
        />
      </template>
      <template #cell-__selection="{ row }">
        <div @click.stop>
          <Checkbox
            data-slot="select-row"
            :model-value="selection.has(rowKey(row))"
            :disabled="loading || deleting"
            :aria-label="`Select ${label(row)}`"
            @update:model-value="(value) => toggle(row, value)"
          />
        </div>
      </template>
      <template v-for="name in forwardedSlots" :key="name" #[name]="scope">
        <slot :name="name" v-bind="scope" />
      </template>
      <template v-for="name in forwardedHeadSlots" :key="name" #[name]="scope">
        <slot :name="name" v-bind="scope" />
      </template>
    </DataTable>
    <Dialog
      :open="pending.length > 0"
      @update:open="
        (value) => {
          if (!value && !deleting) pending = []
        }
      "
    >
      <DialogContent class="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Delete {{ pending.length }} {{ counted(pending.length) }}?</DialogTitle>
          <DialogDescription>{{ deleteDescription }}</DialogDescription>
        </DialogHeader>
        <ul class="max-h-60 space-y-2 overflow-auto rounded-md border p-3 text-sm">
          <li v-for="row in pending" :key="rowKey(row)" class="break-words">
            <span class="font-medium">{{ label(row) }}</span>
            <span v-if="description(row)" class="mt-0.5 block break-all text-xs text-muted-foreground">
              {{ description(row) }}
            </span>
          </li>
        </ul>
        <div v-if="failures.length" role="alert" class="space-y-1 text-sm text-destructive">
          <p>Some deletion requests failed. Successful requests will not be repeated.</p>
          <p v-for="failure in failures" :key="rowKey(failure.row)">{{ label(failure.row) }}: {{ failure.message }}</p>
        </div>
        <label class="grid gap-2 text-sm">
          <span>
            Type
            <strong>{{ confirmText }}</strong>
            to confirm.
          </span>
          <Input v-model="confirmation" :disabled="deleting" aria-label="Confirm deletion" autocomplete="off" />
        </label>
        <DialogFooter>
          <Button variant="outline" :disabled="deleting" @click="pending = []">Cancel</Button>
          <Button variant="destructive" data-slot="confirm-delete" :disabled="deleting || confirmation !== confirmText" @click="confirmDelete">
            {{ deleting ? 'Deleting...' : failures.length ? 'Retry failed deletions' : 'Delete' }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
