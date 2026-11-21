<script setup lang="ts" generic="T">
import { computed, nextTick, onBeforeUnmount, ref, useSlots, watch } from 'vue'
import { ArrowDownIcon, ArrowUpIcon, ChevronDownIcon, ChevronRightIcon, ChevronsUpDownIcon } from '@lucide/vue'
import ClearableSearchInput from './ClearableSearchInput.vue'
import { Button } from '../ui/button'
import { Skeleton } from '../ui/skeleton'
import { Alert, AlertDescription } from '../ui/alert'
import { TableBody, TableCell, TableHead, TableHeader, TableRow } from '../ui/table'

export interface Column<T> {
  key: string
  label: string
  class?: string
  sortable?: boolean
  value?: (row: T) => string | number | undefined
}

const props = withDefaults(defineProps<{
  columns: Column<T>[]
  rows: T[]
  rowKey: (row: T) => string
  loading?: boolean
  error?: string
  emptyText?: string
  searchable?: boolean
  searchPlaceholder?: string
  clickable?: boolean
  rowClass?: string | ((row: T) => string)
  // Which rows get a caret when the `expanded` slot is present. A row the
  // predicate refuses renders without one (its columns already say
  // everything), so the column stays aligned across the table.
  expandable?: (row: T) => boolean
  expandTitle?: (row: T, expanded: boolean) => string
  // Keep the header row in view while the rows scroll. The rows then scroll
  // inside the table, which is at most maxHeight tall; a table shorter than
  // that looks the same either way. Turn it off for a table that should
  // grow with the page (a short form section, a printout).
  stickyHeader?: boolean
  // Any CSS length, or 'available' to fit the space left below the table
  // in the viewport or the nearest bounded scroll parent; used only with
  // stickyHeader.
  maxHeight?: string
  // Infinite scroll. The table renders batchSize rows and adds the next
  // batch as the reader scrolls near the end, so a list of thousands of
  // rows costs what a hundred cost. 0 renders every row at once.
  batchSize?: number
  // For a list the view loads from the server a page at a time: true while
  // the server has more rows than `rows` holds. When the reader reaches the
  // end of what is loaded, the table emits loadMore (once per change of
  // `rows`, and never while `loading`); the view appends the next page.
  hasMore?: boolean
}>(), {
  batchSize: 100,
  hasMore: false,
  stickyHeader: true,
  maxHeight: 'available',
  clickable: false,
  loading: false,
  error: '',
  emptyText: 'Nothing here yet.',
  searchable: true,
  searchPlaceholder: 'Filter...',
  rowClass: '',
  expandTitle: (_row: unknown, expanded: boolean) => expanded ? 'Collapse row' : 'Expand row',
})

const emit = defineEmits<{
  rowClick: [row: T]
  expand: [row: T, expanded: boolean]
  loadMore: []
}>()

const slots = useSlots()

const search = ref('')
const sortKey = ref<string | null>(null)
const sortDir = ref<'asc' | 'desc'>('asc')

function cellValue(col: Column<T>, row: T): string | number | undefined {
  if (col.value) return col.value(row)
  return (row as Record<string, unknown>)[col.key] as string | number | undefined
}

const visible = computed(() => {
  let rows = props.rows
  const q = search.value.trim().toLowerCase()
  if (q) {
    rows = rows.filter(r => props.columns.some(c => String(cellValue(c, r) ?? '').toLowerCase().includes(q)))
  }
  if (sortKey.value) {
    const col = props.columns.find(c => c.key === sortKey.value)
    if (col) {
      const dir = sortDir.value === 'asc' ? 1 : -1
      rows = [...rows].sort((a, b) => {
        const av = cellValue(col, a) ?? ''
        const bv = cellValue(col, b) ?? ''
        if (typeof av === 'number' && typeof bv === 'number') return (av - bv) * dir
        return String(av).localeCompare(String(bv)) * dir
      })
    }
  }
  return rows
})

// Infinite scroll. `rendered` is how many of the visible rows are drawn; a
// sentinel row after them is watched with an IntersectionObserver rooted at
// the scroll container (the viewport when the header is not sticky), and
// coming within a few rows of it draws the next batch or, once every loaded
// row is drawn, asks the view for more.
const rendered = ref(props.batchSize > 0 ? props.batchSize : Number.POSITIVE_INFINITY)
const drawn = computed(() => visible.value.slice(0, rendered.value))
const moreToDraw = computed(() => drawn.value.length < visible.value.length)
const showSentinel = computed(() => moreToDraw.value || (props.hasMore && visible.value.length > 0))

// A new filter or sort starts over from the top; new rows appended by a
// load-more keep what is already drawn.
watch([search, sortKey, sortDir], () => {
  rendered.value = props.batchSize > 0 ? props.batchSize : Number.POSITIVE_INFINITY
})

let requested = false
watch(() => props.rows, () => { requested = false })

function reachedEnd() {
  if (moreToDraw.value) {
    rendered.value += props.batchSize
    return
  }
  if (props.hasMore && !props.loading && !requested) {
    requested = true
    emit('loadMore')
  }
}

const container = ref<HTMLElement | null>(null)

// Available-height sizing. With maxHeight 'available' the scroll container is
// capped at the space between its top and the bottom of the viewport (or of
// the nearest bounded, scrolling ancestor), so a full-page table scrolls
// inside itself rather than pushing the page. The measurement is a maximum:
// a short table still shrinks to its rows.
//
// MIN_AVAILABLE_HEIGHT keeps a table usable when it starts near the bottom
// of a short viewport or dialog; AVAILABLE_BOTTOM_GAP leaves room for the
// container's border and the page padding below it.
const MIN_AVAILABLE_HEIGHT = 160
const AVAILABLE_BOTTOM_GAP = 16
const availableHeight = ref('70vh')
const scrollHeight = computed(() => props.maxHeight === 'available' ? availableHeight.value : props.maxHeight)
let sizeObserver: ResizeObserver | undefined
let resizeFrame = 0

function measureHeight() {
  const element = container.value
  if (!element || props.maxHeight !== 'available' || typeof window === 'undefined') return
  // Ancestor scroll offsets are added back so scrolling the page does not
  // change the measured height (and feed a resize observer loop).
  let top = element.getBoundingClientRect().top
  for (let parent = element.parentElement; parent; parent = parent.parentElement) top += parent.scrollTop
  let bottom = window.visualViewport ? window.visualViewport.offsetTop + window.visualViewport.height : window.innerHeight
  for (let parent = element.parentElement; parent; parent = parent.parentElement) {
    const style = getComputedStyle(parent)
    if (/(auto|scroll|hidden|clip)/.test(style.overflowY) && (parent.scrollHeight > parent.clientHeight + 1 || style.maxHeight !== 'none')) {
      bottom = Math.min(bottom, parent.getBoundingClientRect().bottom - parseFloat(style.paddingBottom || '0'))
    }
  }
  availableHeight.value = `${Math.max(MIN_AVAILABLE_HEIGHT, Math.floor(bottom - top - AVAILABLE_BOTTOM_GAP))}px`
}

function scheduleMeasure() {
  if (typeof window === 'undefined') return
  cancelAnimationFrame(resizeFrame)
  resizeFrame = requestAnimationFrame(measureHeight)
}

watch([container, () => props.maxHeight], () => {
  sizeObserver?.disconnect()
  sizeObserver = undefined
  if (!container.value || props.maxHeight !== 'available' || typeof window === 'undefined') return
  measureHeight()
  if (typeof ResizeObserver !== 'undefined') {
    sizeObserver = new ResizeObserver(scheduleMeasure)
    for (let parent: HTMLElement | null = container.value; parent; parent = parent.parentElement) sizeObserver.observe(parent)
  }
}, { flush: 'post' })

if (typeof window !== 'undefined') {
  window.addEventListener('resize', scheduleMeasure)
  window.visualViewport?.addEventListener('resize', scheduleMeasure)
}
onBeforeUnmount(() => {
  sizeObserver?.disconnect()
  if (typeof window === 'undefined') return
  cancelAnimationFrame(resizeFrame)
  window.removeEventListener('resize', scheduleMeasure)
  window.visualViewport?.removeEventListener('resize', scheduleMeasure)
})

const sentinel = ref<HTMLElement | null>(null)
let observer: IntersectionObserver | undefined

function observe() {
  observer?.disconnect()
  observer = undefined
  if (!sentinel.value || typeof IntersectionObserver === 'undefined') return
  observer = new IntersectionObserver(
    entries => { if (entries.some(e => e.isIntersecting)) reachedEnd() },
    { root: props.stickyHeader ? container.value : null, rootMargin: '0px 0px 400px 0px' },
  )
  observer.observe(sentinel.value)
}

// Re-arm after every draw: an observer only reports changes, so a sentinel
// that is still in view after a batch (a tall container) must be checked
// again or the next batch would never come.
watch([showSentinel, () => drawn.value.length, () => props.rows], () => nextTick(observe), { flush: 'post', immediate: true })
onBeforeUnmount(() => observer?.disconnect())

function toggleSort(col: Column<T>) {
  if (!col.sortable) return
  if (sortKey.value !== col.key) {
    sortKey.value = col.key
    sortDir.value = 'asc'
  } else if (sortDir.value === 'asc') {
    sortDir.value = 'desc'
  } else {
    sortKey.value = null
  }
}

// Keyboard activation of a clickable row: Enter or Space on the focused row
// itself (not on a button, link or checkbox inside it, which keep their own
// behavior). Space's page scroll is suppressed only when it activates a row.
function activate(row: T, event: KeyboardEvent) {
  if (!props.clickable) return
  event.preventDefault()
  emit('rowClick', row)
}

function classesFor(row: T): string {
  const own = typeof props.rowClass === 'function' ? props.rowClass(row) : props.rowClass
  return [props.clickable ? 'cursor-pointer outline-none focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:[&>td:first-child]:rounded-l-md' : '', own].filter(Boolean).join(' ')
}

// Row expansion. The `expanded` slot renders a full-width row beneath a data
// row; the leading caret column exists only when the slot is given, so tables
// without it are laid out exactly as before. State is keyed by rowKey rather
// than by object identity because a refresh replaces every row object.
const hasExpansion = computed(() => !!slots.expanded)
const expanded = ref(new Set<string>())

function canExpand(row: T): boolean {
  return hasExpansion.value && (props.expandable ? props.expandable(row) : true)
}

function isExpanded(row: T): boolean {
  return expanded.value.has(props.rowKey(row))
}

function toggleExpanded(row: T) {
  const key = props.rowKey(row)
  const next = new Set(expanded.value)
  const open = !next.delete(key)
  if (open) next.add(key)
  expanded.value = next
  emit('expand', row, open)
}

// Drop expansion state for rows that are no longer listed rather than holding
// keys for deleted items.
watch(() => props.rows, rows => {
  if (!expanded.value.size) return
  const keys = new Set(rows.map(props.rowKey))
  expanded.value = new Set([...expanded.value].filter(key => keys.has(key)))
})

const spannedColumns = computed(() =>
  props.columns.length + (hasExpansion.value ? 1 : 0) + (slots.actions ? 1 : 0))
</script>

<template>
  <div class="flex flex-col gap-3">
    <div v-if="searchable || $slots.toolbar" class="flex flex-wrap items-center gap-2">
      <ClearableSearchInput
        v-if="searchable"
        v-model="search"
        :placeholder="searchPlaceholder"
        aria-label="Filter rows"
        clear-label="Clear filter"
        class="w-64 max-w-full"
      />
      <slot name="toolbar" />
      <span class="ml-auto text-xs tabular-nums text-muted-foreground">
        {{ visible.length }}<template v-if="visible.length !== rows.length"> of {{ rows.length }}</template>
      </span>
    </div>

    <Alert v-if="error" variant="destructive">
      <AlertDescription>{{ error }}</AlertDescription>
    </Alert>

    <!--
      One scroll container for both axes. With stickyHeader it is bounded in
      height so the header cells (position: sticky) stay at its top while the
      rows scroll; a second, horizontal-only scroll container inside it would
      capture the sticky header, so the table is rendered directly here
      rather than through the Table primitive.
    -->
    <div
      ref="container"
      class="relative overflow-auto rounded-lg border"
      data-slot="table-container"
      :data-sticky-header="stickyHeader || undefined"
      :style="stickyHeader ? { maxHeight: scrollHeight } : undefined"
    >
      <table data-slot="table" class="w-full caption-bottom text-sm">
        <TableHeader :class="stickyHeader && 'sticky top-0 z-10 bg-background [&_th]:shadow-[inset_0_-1px_0_var(--border)]'">
          <TableRow class="hover:bg-transparent">
            <TableHead v-if="hasExpansion" class="w-8" />
            <TableHead
              v-for="col in columns"
              :key="col.key"
              :class="[col.class, col.sortable ? 'cursor-pointer select-none hover:text-foreground' : '']"
              @click="toggleSort(col)"
            >
              <slot :name="`head-${col.key}`" :column="col">
                <span class="inline-flex items-center gap-1">
                  {{ col.label }}
                  <template v-if="col.sortable">
                    <ArrowUpIcon v-if="sortKey === col.key && sortDir === 'asc'" class="size-3.5" />
                    <ArrowDownIcon v-else-if="sortKey === col.key" class="size-3.5" />
                    <ChevronsUpDownIcon v-else class="size-3.5 opacity-40" />
                  </template>
                </span>
              </slot>
            </TableHead>
            <TableHead v-if="$slots.actions" class="w-10" />
          </TableRow>
        </TableHeader>
        <TableBody>
          <template v-if="loading && rows.length === 0">
            <TableRow v-for="i in 5" :key="i">
              <TableCell v-if="hasExpansion" />
              <TableCell v-for="col in columns" :key="col.key"><Skeleton class="h-4 w-24" /></TableCell>
              <TableCell v-if="$slots.actions" />
            </TableRow>
          </template>
          <TableRow v-else-if="visible.length === 0">
            <TableCell :colspan="spannedColumns" class="h-24 text-center text-muted-foreground">
              {{ search ? 'No rows match the filter.' : emptyText }}
            </TableCell>
          </TableRow>
          <template v-for="row in drawn" v-else :key="rowKey(row)">
            <TableRow
              :class="classesFor(row)"
              :tabindex="clickable ? 0 : undefined"
              @click="clickable && emit('rowClick', row)"
              @keydown.enter.self="activate(row, $event)"
              @keydown.space.self="activate(row, $event)"
            >
              <TableCell v-if="hasExpansion" class="pr-0" @click.stop>
                <Button
                  v-if="canExpand(row)"
                  variant="ghost"
                  size="icon"
                  class="size-6"
                  data-slot="expand-toggle"
                  :title="expandTitle(row, isExpanded(row))"
                  :aria-expanded="isExpanded(row)"
                  @click="toggleExpanded(row)"
                >
                  <ChevronDownIcon v-if="isExpanded(row)" class="size-4" />
                  <ChevronRightIcon v-else class="size-4" />
                </Button>
              </TableCell>
              <TableCell v-for="col in columns" :key="col.key" :class="col.class">
                <slot :name="`cell-${col.key}`" :row="row" :value="cellValue(col, row)">
                  {{ cellValue(col, row) }}
                </slot>
              </TableCell>
              <TableCell v-if="$slots.actions" class="text-right" @click.stop>
                <slot name="actions" :row="row" />
              </TableCell>
            </TableRow>
            <TableRow v-if="hasExpansion && isExpanded(row)" data-slot="expanded-row" class="hover:bg-transparent">
              <TableCell :colspan="spannedColumns" class="bg-muted/20 p-4">
                <slot name="expanded" :row="row" />
              </TableCell>
            </TableRow>
          </template>
          <TableRow v-if="showSentinel" class="hover:bg-transparent" data-slot="load-more">
            <TableCell :colspan="spannedColumns" class="p-0">
              <div ref="sentinel" class="py-2 text-center text-xs text-muted-foreground">
                {{ moreToDraw ? 'Showing more rows...' : loading ? 'Loading more...' : '' }}
              </div>
            </TableCell>
          </TableRow>
        </TableBody>
      </table>
    </div>
  </div>
</template>
