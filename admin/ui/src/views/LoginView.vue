<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { LoginCard } from '@bboxsys/ui'
import { useSessionStore } from '@/stores/session'
import { oauthApi } from '@/api/admin'
import { useBrandingStore } from '@/stores/branding'
import logo from '@/assets/logo.svg'

const session = useSessionStore()
const brand = useBrandingStore()
const route = useRoute()
const router = useRouter()

const error = ref('')
const oauthEnabled = ref(false)

onMounted(async () => {
  brand.load()
  try {
    const cfg = await oauthApi.config()
    oauthEnabled.value = !!cfg.enabled
  } catch {
    oauthEnabled.value = false
  }
})

function done() {
  const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : '/'
  router.replace(redirect)
}

async function google() {
  error.value = ''
  try {
    const res = await oauthApi.authorize()
    sessionStorage.setItem('bboxsys.oauth.state', res.state)
    window.location.href = res.authorization_url
  } catch (e) {
    error.value = (e as Error).message
  }
}
</script>

<template>
  <LoginCard
    :session="session"
    :title="`${brand.name} Admin`"
    description="Sign in to manage accounts and users"
    :oauth-enabled="oauthEnabled"
    @success="done"
    @oauth="google"
  >
    <template #logo>
      <img v-if="brand.logoUrl" :src="brand.logoUrl" :alt="brand.name" class="mx-auto mb-2 size-12 rounded-lg" />
      <img v-else-if="brand.isDefault" :src="logo" alt="Black Box" class="mx-auto mb-2 size-12" />
      <div v-else class="mx-auto mb-2 flex size-12 items-center justify-center rounded-lg bg-primary text-lg font-bold text-primary-foreground">{{ brand.monogram }}</div>
    </template>
    <p v-if="error" class="mt-4 text-center text-xs text-destructive">{{ error }}</p>
  </LoginCard>
</template>
