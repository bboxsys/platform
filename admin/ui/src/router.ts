import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { useSessionStore } from '@/stores/session'
import { useBrandingStore } from '@/stores/branding'

declare module 'vue-router' {
  interface RouteMeta {
    title?: string
    public?: boolean
    superAdmin?: boolean
    layout?: 'shell' | 'bare'
  }
}

const routes: RouteRecordRaw[] = [
  { path: '/', redirect: '/accounts' },
  { path: '/login', component: () => import('@/views/LoginView.vue'), meta: { title: 'Sign in', public: true, layout: 'bare' } },
  { path: '/oauth/callback', component: () => import('@/views/OAuthCallbackView.vue'), meta: { title: 'Signing in', public: true, layout: 'bare' } },
  { path: '/invite/accept', component: () => import('@/views/InviteAcceptView.vue'), meta: { title: 'Accept invitation', public: true, layout: 'bare' } },
  { path: '/accounts', component: () => import('@/views/AccountsView.vue'), meta: { title: 'Accounts', superAdmin: true } },
  { path: '/accounts/new', component: () => import('@/views/AccountNewView.vue'), meta: { title: 'New account', superAdmin: true } },
  {
    path: '/accounts/:accountId',
    component: () => import('@/views/AccountView.vue'),
    meta: { title: 'Account' },
    children: [
      { path: '', redirect: to => `/accounts/${to.params.accountId}/users` },
      { path: 'users', component: () => import('@/views/account/UsersTab.vue') },
      { path: 'invitations', component: () => import('@/views/account/InvitationsTab.vue') },
      { path: 'sso', component: () => import('@/views/account/IdentityProviderTab.vue') },
      { path: 'services', component: () => import('@/views/account/ServicesTab.vue'), meta: { superAdmin: true } },
      { path: 'agents', component: () => import('@/views/account/AgentsTab.vue'), meta: { superAdmin: true } },
    ],
  },
  { path: '/accounts/:accountId/users/new', component: () => import('@/views/UserNewView.vue'), meta: { title: 'New user' } },
  { path: '/accounts/:accountId/users/:userId', component: () => import('@/views/UserView.vue'), meta: { title: 'User' } },
  { path: '/accounts/:accountId/invitations/new', component: () => import('@/views/InvitationNewView.vue'), meta: { title: 'New invitation' } },
  { path: '/accounts/:accountId/services/new', component: () => import('@/views/ServiceNewView.vue'), meta: { title: 'New service', superAdmin: true } },
  { path: '/accounts/:accountId/agents/new', component: () => import('@/views/AgentNewView.vue'), meta: { title: 'New agent', superAdmin: true } },
  { path: '/pki', component: () => import('@/views/PKIView.vue'), meta: { title: 'PKI', superAdmin: true } },
  { path: '/operations/services', component: () => import('@/views/operations/ServicesView.vue'), meta: { title: 'Services', superAdmin: true } },
  { path: '/operations/logs', component: () => import('@/views/operations/LogsView.vue'), meta: { title: 'Logs', superAdmin: true } },
  { path: '/operations/stores', component: () => import('@/views/operations/StoreMetricsView.vue'), meta: { title: 'Store metrics', superAdmin: true } },
  { path: '/operations/queue', component: () => import('@/views/operations/QueueView.vue'), meta: { title: 'Queue', superAdmin: true } },
  { path: '/utilities', component: () => import('@/views/UtilitiesView.vue'), meta: { title: 'API explorer' } },
  { path: '/profile', component: () => import('@/views/ProfileView.vue'), meta: { title: 'Profile' } },
  { path: '/credentials', component: () => import('@/views/CredentialsView.vue'), meta: { title: 'Credentials' } },
  // Old name of the credentials page, kept so bookmarks and links still work.
  { path: '/tokens', redirect: '/credentials' },
  { path: '/assistant', component: () => import('@/views/AssistantView.vue'), meta: { title: 'Assistant' } },
  { path: '/ai/usage', component: () => import('@/views/ai/UsageView.vue'), meta: { title: 'AI usage', superAdmin: true } },
  { path: '/:pathMatch(.*)*', redirect: '/' },
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
})

router.beforeEach(to => {
  const session = useSessionStore()
  if (to.meta.public) return true
  if (!session.isAuthenticated) return { path: '/login', query: { redirect: to.fullPath } }

  const needsSuperAdmin = to.matched.some(r => r.meta.superAdmin)
  if (needsSuperAdmin && !session.isSuperAdmin) return `/accounts/${session.accountId}`

  // Tenant administrators only ever see their own account.
  const accountParam = to.params.accountId
  if (!session.isSuperAdmin && typeof accountParam === 'string' && accountParam !== session.accountId) {
    return `/accounts/${session.accountId}`
  }
  if (to.path === '/' || to.path === '/accounts') {
    if (!session.isSuperAdmin) return `/accounts/${session.accountId}`
  }
  return true
})

router.afterEach(to => {
  document.title = useBrandingStore().documentTitle(to.meta.title)
})
