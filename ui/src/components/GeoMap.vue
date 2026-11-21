<script lang="ts">
// tooltipContent builds tooltip content as a DOM element with textContent set,
// never as an HTML string. Leaflet renders a string tooltip via innerHTML, so
// passing user-controlled labels (waypoint/route/lake names) as strings is a
// stored-XSS vector; a text node cannot inject markup. Exported so the escaping
// invariant can be unit-tested without mounting Leaflet.
export function tooltipContent(text: string): HTMLSpanElement {
  const span = document.createElement('span')
  span.textContent = text
  return span
}
</script>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useDark, useResizeObserver } from '@vueuse/core'
import type * as LT from 'leaflet'

export interface MapMarker {
  id: string
  lat: number
  lon: number
  color?: string
  label?: string
}

export interface MapLine {
  id: string
  points: [number, number][]
  color?: string
  label?: string
}

export interface MapPolygon {
  id: string
  points: [number, number][]
  color?: string
  label?: string
}

const props = withDefaults(defineProps<{
  markers?: MapMarker[]
  lines?: MapLine[]
  polygons?: MapPolygon[]
  selectedId?: string | null
  fitKey?: string | number
  tile?: 'osm' | 'satellite' | 'topo'
}>(), { markers: () => [], lines: () => [], polygons: () => [], selectedId: null, fitKey: 0, tile: 'osm' })

const emit = defineEmits<{ select: [id: string, kind: 'marker' | 'line' | 'polygon']; mapClick: [lat: number, lon: number] }>()

const TILES = {
  osm: { url: 'https://tile.openstreetmap.org/{z}/{x}/{y}.png', attribution: '&copy; OpenStreetMap contributors', maxZoom: 19 },
  satellite: {
    url: 'https://server.arcgisonline.com/ArcGIS/rest/services/World_Imagery/MapServer/tile/{z}/{y}/{x}',
    attribution: 'Tiles &copy; Esri',
    maxZoom: 19,
  },
  topo: { url: 'https://{s}.tile.opentopomap.org/{z}/{x}/{y}.png', attribution: '&copy; OpenTopoMap', maxZoom: 17 },
}

const el = ref<HTMLElement | null>(null)
const isDark = useDark()

// Leaflet loads lazily so apps that import the package index but never
// render a map do not ship it, and map pages pull it in parallel with data.
let L: typeof LT | undefined
let map: LT.Map | undefined
let tileLayer: LT.TileLayer | undefined
let markerLayer: LT.LayerGroup | undefined
let lineLayer: LT.LayerGroup | undefined
let polygonLayer: LT.LayerGroup | undefined
const markerRefs = new Map<string, LT.CircleMarker>()
// All features by id, used to restyle the selection without rebuilding the map.
const featureRefs = new Map<string, { layer: LT.Path; kind: 'marker' | 'line' | 'polygon' }>()

// applySelectionStyle restyles a single feature for the selected/unselected
// state in place (setStyle/setRadius), which is O(1) per feature instead of
// tearing down and recreating every layer on each selection change.
function applySelectionStyle(ref: { layer: LT.Path; kind: 'marker' | 'line' | 'polygon' }, selected: boolean) {
  if (ref.kind === 'marker') {
    const mk = ref.layer as LT.CircleMarker
    mk.setRadius(selected ? 9 : 5)
    mk.setStyle({ color: selected ? '#ffffff' : 'rgba(255,255,255,.85)' })
  } else if (ref.kind === 'line') {
    ref.layer.setStyle({ weight: selected ? 5 : 3, opacity: selected ? 1 : 0.8 })
  } else {
    ref.layer.setStyle({ weight: selected ? 3 : 1.5, fillOpacity: selected ? 0.2 : 0.08 })
  }
}

function applySelection() {
  for (const [id, ref] of featureRefs) applySelectionStyle(ref, id === props.selectedId)
}

function render() {
  if (!map || !L) return
  markerLayer!.clearLayers()
  lineLayer!.clearLayers()
  polygonLayer!.clearLayers()
  markerRefs.clear()
  featureRefs.clear()
  for (const m of props.markers) {
    // Canvas circle markers, not divIcon DOM markers: with 10k+ waypoints,
    // one <div> per point melts the main thread on every pan/zoom. The
    // canvas renderer (preferCanvas on the map) paints them all in one
    // layer and keeps per-marker click/tooltip behavior.
    const selected = m.id === props.selectedId
    const mk = L.circleMarker([m.lat, m.lon], {
      radius: selected ? 9 : 5,
      color: selected ? '#ffffff' : 'rgba(255,255,255,.85)',
      weight: 2,
      fillColor: m.color || '#3b82f6',
      fillOpacity: 1,
    })
    if (m.label) mk.bindTooltip(tooltipContent(m.label), { direction: 'top', offset: [0, -8], className: 'geo-tooltip' })
    mk.on('click', () => emit('select', m.id, 'marker'))
    mk.addTo(markerLayer!)
    markerRefs.set(m.id, mk)
    featureRefs.set(m.id, { layer: mk, kind: 'marker' })
  }
  for (const ln of props.lines) {
    const selected = ln.id === props.selectedId
    const pl = L.polyline(ln.points, { color: ln.color || '#f97316', weight: selected ? 5 : 3, opacity: selected ? 1 : 0.8 })
    if (ln.label) pl.bindTooltip(tooltipContent(ln.label), { sticky: true, className: 'geo-tooltip' })
    pl.on('click', () => emit('select', ln.id, 'line'))
    pl.addTo(lineLayer!)
    featureRefs.set(ln.id, { layer: pl, kind: 'line' })
  }
  for (const pg of props.polygons) {
    const selected = pg.id === props.selectedId
    const poly = L.polygon(pg.points, { color: pg.color || '#0ea5e9', weight: selected ? 3 : 1.5, fillOpacity: selected ? 0.2 : 0.08 })
    if (pg.label) poly.bindTooltip(tooltipContent(pg.label), { sticky: true, className: 'geo-tooltip' })
    poly.on('click', () => emit('select', pg.id, 'polygon'))
    poly.addTo(polygonLayer!)
    featureRefs.set(pg.id, { layer: poly, kind: 'polygon' })
  }
}

function fit() {
  if (!map || !L) return
  const pts: LT.LatLngExpression[] = []
  for (const m of props.markers) pts.push([m.lat, m.lon])
  for (const ln of props.lines) pts.push(...ln.points)
  for (const pg of props.polygons) pts.push(...pg.points)
  if (pts.length === 0) return
  if (pts.length === 1) {
    map.setView(pts[0]!, 14)
    return
  }
  map.fitBounds(L.latLngBounds(pts), { padding: [24, 24], maxZoom: 16 })
}

function setTiles() {
  if (!map || !L) return
  tileLayer?.remove()
  const t = TILES[props.tile]
  tileLayer = L.tileLayer(t.url, { attribution: t.attribution, maxZoom: t.maxZoom }).addTo(map)
}

let disposed = false

onMounted(async () => {
  L = await import('leaflet')
  if (disposed || !el.value) return
  map = L.map(el.value, { center: [31.5, -96], zoom: 6, zoomControl: true, preferCanvas: true })
  setTiles()
  polygonLayer = L.layerGroup().addTo(map)
  lineLayer = L.layerGroup().addTo(map)
  markerLayer = L.layerGroup().addTo(map)
  map.on('click', e => emit('mapClick', e.latlng.lat, e.latlng.lng))
  render()
  fit()
})

watch(() => [props.markers, props.lines, props.polygons], () => render(), { deep: false })
watch(() => props.fitKey, () => fit())
watch(() => props.tile, setTiles)
watch(() => props.selectedId, id => {
  // Restyle in place instead of rebuilding every layer; only pan to the
  // newly selected marker.
  applySelection()
  const mk = id ? markerRefs.get(id) : undefined
  if (mk && map) map.panTo(mk.getLatLng())
})
useResizeObserver(el, () => map?.invalidateSize())
onBeforeUnmount(() => {
  disposed = true
  map?.remove()
  map = undefined
  markerRefs.clear()
  featureRefs.clear()
})

defineExpose({ fit })
</script>

<template>
  <!-- isolate: Leaflet's internal panes use z-indexes up to 1000, which
       would otherwise stack above page-level popovers (selects, dropdowns,
       dialogs) that sit at z-50. A contained stacking context keeps all of
       Leaflet's layering inside this element. -->
  <div ref="el" class="isolate z-0 h-full w-full bg-muted" :class="{ 'geo-dark': isDark && tile !== 'satellite' }" />
</template>

<style>
.geo-dark .leaflet-tile { filter: brightness(0.7) contrast(1.1) saturate(0.6); }
.geo-tooltip {
  background: var(--popover);
  color: var(--popover-foreground);
  border: 1px solid var(--border);
  border-radius: 6px;
  box-shadow: 0 4px 12px rgba(0, 0, 0, .25);
  font-size: 12px;
  padding: 4px 8px;
}
.geo-tooltip::before { display: none; }
.leaflet-container { font-family: inherit; }
</style>
