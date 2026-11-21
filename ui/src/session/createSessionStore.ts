import { computed, ref, watch } from 'vue'
import { defineStore } from 'pinia'
import { useDark } from '@vueuse/core'
import { request, type RequestAuth } from '../lib/http'
import type { LoginResponse, ServiceEndpoint, User } from '../types/admin'

export interface StoredSession {
  user: User
  token: string
  accountId: string
  isSuperAdmin: boolean
  // Base URL the token was issued by; '' means same-origin.
  loginService: string
  serviceEndpoints: Record<string, ServiceEndpoint>
}

export interface SessionStoreOptions {
  // localStorage key. Each app uses its own so sessions never leak between
  // apps that happen to share an origin during development.
  storageKey: string
  // Base URL of the admin server when the SPA talks to it directly, as the
  // admin console does ('' for same-origin, or VITE_ADMIN_URL in development).
  // Leave unset for SPAs served by another service: the store then discovers
  // the login service from /serviceendpoints/ on its own origin.
  adminBase?: () => string
  // Name used in the error shown when no login service URL can be found.
  serverName?: string
  // Pinia store id. Defaults to 'session'.
  id?: string
}

// createSessionStore builds the Pinia session store every bboxsys SPA uses:
// login against the admin server with basic auth, the resulting NATS user JWT
// and account id as RequestAuth for API calls, the service endpoint map, and
// the user's saved theme preference. Apps wrap it once:
//
//   export const useSessionStore = createSessionStore({ storageKey: 'myapp.session' })
export function createSessionStore(opts: SessionStoreOptions) {
  const { storageKey, adminBase, serverName = 'admin', id = 'session' } = opts

  function load(): StoredSession | null {
    try {
      const raw = localStorage.getItem(storageKey)
      return raw ? (JSON.parse(raw) as StoredSession) : null
    } catch {
      return null
    }
  }

  return defineStore(id, () => {
    const session = ref<StoredSession | null>(load())
    const endpoints = ref<Record<string, ServiceEndpoint>>(session.value?.serviceEndpoints ?? {})
    const bootstrapped = ref(false)
    const isDark = useDark()

    const user = computed(() => session.value?.user ?? null)
    const accountId = computed(() => session.value?.accountId ?? '')
    const isSuperAdmin = computed(() => session.value?.isSuperAdmin ?? false)
    const isAuthenticated = computed(() => !!session.value?.token)
    const isTenantAdmin = computed(() =>
      (user.value?.roles ?? []).some(r => r === 'administrator' || r === 'administrators'),
    )
    const canAdminister = computed(() => isSuperAdmin.value || isTenantAdmin.value)

    const auth = computed<RequestAuth>(() => ({
      token: session.value?.token,
      accountId: session.value?.accountId,
    }))

    watch(session, s => {
      try {
        if (s) localStorage.setItem(storageKey, JSON.stringify(s))
        else localStorage.removeItem(storageKey)
      } catch {
        // storage unavailable; the session lives in memory only
      }
    }, { deep: true })

    function endpoint(kind: string): string {
      return endpoints.value[kind]?.url ?? ''
    }

    // The admin console knows its server; every other SPA finds the login
    // service through the endpoint map.
    function loginBase(): string {
      return adminBase ? adminBase() : endpoint('login_service')
    }

    async function bootstrapEndpoints() {
      if (bootstrapped.value) return
      try {
        endpoints.value = await request<Record<string, ServiceEndpoint>>(`${adminBase?.() ?? ''}/serviceendpoints/`)
        bootstrapped.value = true
      } catch {
        // keep the endpoints saved with the session, if any
      }
    }

    async function login(email: string, password: string) {
      await bootstrapEndpoints()
      const base = loginBase()
      if (!adminBase && !base) {
        throw new Error(`Login service URL is not available. Is the ${serverName} server running?`)
      }
      const res = await request<LoginResponse>(`${base}/api/v1/login`, { auth: { basic: { email, password } } })
      adopt(res, base)
    }

    // adopt installs a login response obtained elsewhere (OAuth callback,
    // invitation acceptance) as the current session.
    function adopt(res: LoginResponse, loginService: string = loginBase()) {
      const eps = res.service_endpoints ?? {}
      if (Object.keys(eps).length) endpoints.value = eps
      session.value = {
        user: res.user,
        token: res.token,
        accountId: res.user.account_id,
        isSuperAdmin: !!res.is_super_admin,
        loginService,
        serviceEndpoints: endpoints.value,
      }
      applyTheme()
    }

    function logout() {
      session.value = null
    }

    function patchUser(patch: Partial<User>) {
      if (session.value) session.value.user = { ...session.value.user, ...patch }
    }

    // The theme preference lives on the user record in the admin service.
    // 'system' and '' leave the browser preference alone.
    function applyTheme() {
      const t = user.value?.theme_preference
      if (t === 'dark') isDark.value = true
      else if (t === 'light') isDark.value = false
    }

    applyTheme()

    return {
      session, endpoints, user, accountId, isSuperAdmin, isTenantAdmin, canAdminister, isAuthenticated, auth,
      endpoint, loginBase, bootstrapEndpoints, login, adopt, logout, patchUser, applyTheme,
    }
  })
}

// SessionStore is the instance type returned by the store created above, for
// components that receive the store as a prop.
export type SessionStore = ReturnType<ReturnType<typeof createSessionStore>>
