// Shared libraries
export * from './lib/http'
export * from './lib/format'
export * from './lib/geo'
export { cn } from './lib/utils'
export * from './composables/useAsync'
export * from './composables/usePolling'
export * from './composables/usePagedList'
export * from './lib/objectTable'
export * from './lib/apitokenAuth'
export * from './lib/aiCredential'
// The assistant conversation API client (sessions, streamed runs, the
// selectable assistants); the components below render it.
export * from './lib/assistantApi'
export * from './lib/docsSession'
export * from './lib/httpStatus'
// Opt-in: an app attaches focusPageScroll to its content root's click so
// keyboard scrolling returns to the page after a background click.
export * from './lib/pageScrollFocus'

// Theme: the token bridge for canvas renderers, chart and tone helpers, and
// the persisted light/dark mode. The stylesheet itself is
// `@bboxsys/ui/theme.css`, imported from the app's CSS.
export * from './theme/tokens'
export * from './theme/chartTheme'
export * from './theme/tone'
export * from './theme/useTheme'

// Admin server API types and the session store every app signs in with
export * from './types/admin'
export * from './session/createSessionStore'

// Components
export { default as DataTable } from './components/DataTable.vue'
export type { Column } from './components/DataTable.vue'
// The standard table: a DataTable under the shared toolbar, with selection,
// bulk delete and paging. ObjectAction and ClearableSearchInput are the
// toolbar's own parts, for a view that lays out a toolbar without a table.
export { default as ObjectTable } from './components/ObjectTable.vue'
export { default as ObjectAction } from './components/ObjectAction.vue'
export type { ObjectActionKind } from './components/ObjectAction.vue'
export { default as ClearableSearchInput } from './components/ClearableSearchInput.vue'
export { default as ConfirmDialog } from './components/ConfirmDialog.vue'
export { default as PageHeader } from './components/PageHeader.vue'
export { default as PageTabs } from './components/PageTabs.vue'
export type { PageTab } from './components/PageTabs.vue'
export { default as StatusBadge } from './components/StatusBadge.vue'
export { default as AccentSurface } from './components/AccentSurface.vue'
export { default as AppNotice } from './components/AppNotice.vue'
export { default as AppNoticeBand } from './components/AppNoticeBand.vue'
export { default as MetricTile } from './components/MetricTile.vue'
export { default as TermList } from './components/TermList.vue'
export { default as GeoMap } from './components/GeoMap.vue'
export type { MapLine, MapMarker, MapPolygon } from './components/GeoMap.vue'
export { default as ApiExplorer } from './components/api-explorer/ApiExplorer.vue'
export type { ApiService } from './components/api-explorer/ApiExplorer.vue'
export type { OpenAPISpec } from './components/api-explorer/openapi'
export { default as LoginCard } from './components/LoginCard.vue'
export { default as ServiceApiExplorer } from './components/ServiceApiExplorer.vue'
export { default as ApiTokenAuthFields } from './components/ApiTokenAuthFields.vue'
export { default as ThemeToggle } from './components/ThemeToggle.vue'
// The AI assistant: a conversation with one assistant (page or panel
// layout), the drawer that hosts it beside an app's views with a picker
// for which assistant answers, and the Markdown-with-charts renderer of a
// reply.
export { default as AssistantChat } from './components/assistant/AssistantChat.vue'
export { default as AssistantDrawer } from './components/assistant/AssistantDrawer.vue'
export { default as AssistantMarkdown } from './components/assistant/AssistantMarkdown.vue'
export { default as DocsGate } from './components/DocsGate.vue'
