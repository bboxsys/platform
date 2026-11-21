import { createSessionStore } from '@bboxsys/ui'

export type { StoredSession } from '@bboxsys/ui'

// The SPA is served by the admin server, so the API is same-origin by
// default. A different origin can be used when developing against a remote
// admin service.
export function adminBase(): string {
  return import.meta.env.VITE_ADMIN_URL || ''
}

export const useSessionStore = createSessionStore({
  storageKey: 'bboxsys.admin.session',
  adminBase,
  serverName: 'admin',
})
