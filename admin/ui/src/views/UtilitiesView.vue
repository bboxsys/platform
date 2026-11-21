<script setup lang="ts">
import { ServiceApiExplorer, type ApiService } from '@bboxsys/ui'
import { adminBase, useSessionStore } from '@/stores/session'

const session = useSessionStore()

// The admin service is same-origin and always listed first; the endpoint map
// already contains it, so it is excluded there to avoid a duplicate entry.
const adminService: ApiService[] = [
  { name: 'admin_service', baseUrl: adminBase(), specUrl: `${adminBase()}/api/v1/openapi.json` },
]
</script>

<template>
  <ServiceApiExplorer
    :session="session"
    :include="adminService"
    :exclude="['admin_service', 'login_service', 'controller_service']"
  />
</template>
