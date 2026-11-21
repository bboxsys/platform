import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { setUnauthorizedHandler } from '@bboxsys/ui'
import App from './App.vue'
import { router } from './router'
import { useSessionStore } from './stores/session'
import './style.css'

const app = createApp(App)
app.use(createPinia())
app.use(router)

// A bearer-authenticated request returning 401 means the session token has
// expired or been revoked. Clear it and send the user to the login page rather
// than leaving them on a shell whose every data load fails with "unauthorized".
setUnauthorizedHandler(() => {
  const session = useSessionStore()
  if (!session.isAuthenticated) return
  session.logout()
  const current = router.currentRoute.value
  if (current.path !== '/login') {
    router.replace({ path: '/login', query: { redirect: current.fullPath } })
  }
})

app.mount('#app')
