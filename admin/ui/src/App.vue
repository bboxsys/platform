<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'
import {
  ActivityIcon, BotIcon, BuildingIcon, DatabaseIcon, ExternalLinkIcon, GaugeIcon, KeyRoundIcon, ListChecksIcon, LogOutIcon,
  ScrollTextIcon, ServerIcon, ShieldCheckIcon, SquareTerminalIcon, UserIcon,
} from '@lucide/vue'
import logo from '@/assets/logo.svg'
import { Toaster } from '@/components/ui/sonner'
import { Separator } from '@/components/ui/separator'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  Sidebar, SidebarContent, SidebarFooter, SidebarGroup, SidebarGroupContent, SidebarGroupLabel, SidebarHeader,
  SidebarInset, SidebarMenu, SidebarMenuButton, SidebarMenuItem, SidebarProvider, SidebarRail, SidebarTrigger,
} from '@/components/ui/sidebar'
import { useSessionStore } from '@/stores/session'
import { useBrandingStore } from '@/stores/branding'
import { focusPageScroll, outstanding, ThemeToggle } from '@bboxsys/ui'

const route = useRoute()
const router = useRouter()
const session = useSessionStore()
const brand = useBrandingStore()

onMounted(() => brand.load())

// Link back to the main app. Derived from the admin host
// (admin.example.com -> app.example.com); falls back to the same origin.
const appUrl = computed(() => {
  const { protocol, host } = window.location
  if (host.startsWith('admin.')) return `${protocol}//app.${host.slice('admin.'.length)}`
  return `${protocol}//${host}`
})

const bare = computed(() => route.meta.layout === 'bare')

const nav = computed(() => {
  const acct = session.accountId
  const groups = [
    {
      label: 'Tenant',
      items: session.isSuperAdmin
        ? [{ to: '/accounts', label: 'Accounts', icon: BuildingIcon, match: (p: string) => p.startsWith('/accounts') }]
        : [{ to: `/accounts/${acct}`, label: 'My account', icon: BuildingIcon, match: (p: string) => p.startsWith('/accounts') }],
    },
  ]
  if (session.isSuperAdmin) {
    groups.push({
      label: 'Platform',
      items: [
        { to: '/pki', label: 'PKI', icon: ShieldCheckIcon, match: p => p.startsWith('/pki') },
        { to: '/operations/services', label: 'Services', icon: ServerIcon, match: p => p.startsWith('/operations/services') },
        { to: '/operations/logs', label: 'Logs', icon: ScrollTextIcon, match: p => p.startsWith('/operations/logs') },
        { to: '/operations/stores', label: 'Store metrics', icon: DatabaseIcon, match: p => p.startsWith('/operations/stores') },
        { to: '/operations/queue', label: 'Queue', icon: ListChecksIcon, match: p => p.startsWith('/operations/queue') },
      ],
    })
  }
  groups.push({
    label: 'Utilities',
    items: [
      { to: '/utilities', label: 'API explorer', icon: SquareTerminalIcon, match: p => p.startsWith('/utilities') },
    ],
  })
  groups.push({
    label: 'AI',
    items: [
      { to: '/assistant', label: 'Assistant', icon: BotIcon, match: p => p === '/assistant' },
      ...(session.isSuperAdmin
        ? [{ to: '/ai/usage', label: 'Usage', icon: GaugeIcon, match: (p: string) => p.startsWith('/ai/usage') }]
        : []),
    ],
  })
  return groups
})

const initials = computed(() => {
  const u = session.user
  if (!u) return '?'
  const a = (u.firstname?.[0] ?? '') + (u.lastname?.[0] ?? '')
  return (a || u.email?.[0] || '?').toUpperCase()
})

function logout() {
  session.logout()
  router.push('/login')
}
</script>

<template>
  <Teleport to="body">
    <Toaster position="bottom-right" rich-colors />
  </Teleport>

  <template v-if="bare">
    <RouterView />
  </template>

  <SidebarProvider v-else>
    <Sidebar collapsible="icon">
      <SidebarHeader>
        <div class="flex items-center gap-2 px-1 py-1">
          <img v-if="brand.logoUrl" :src="brand.logoUrl" :alt="brand.name" class="size-8 shrink-0 rounded-md" />
          <img v-else-if="brand.isDefault" :src="logo" alt="Black Box" class="size-8 shrink-0" />
          <div v-else class="flex size-8 shrink-0 items-center justify-center rounded-md bg-primary text-xs font-bold text-primary-foreground">{{ brand.monogram }}</div>
          <div class="min-w-0 group-data-[collapsible=icon]:hidden">
            <div class="truncate text-sm font-semibold">{{ brand.name }} Admin</div>
            <div class="truncate text-xs text-muted-foreground">{{ session.isSuperAdmin ? 'Super admin' : 'Account admin' }}</div>
          </div>
        </div>
      </SidebarHeader>
      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupContent>
            <SidebarMenu>
              <SidebarMenuItem>
                <SidebarMenuButton as-child :tooltip="`${brand.name} App`">
                  <a :href="appUrl">
                    <ExternalLinkIcon />
                    <span>{{ brand.name }} App</span>
                  </a>
                </SidebarMenuButton>
              </SidebarMenuItem>
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
        <SidebarGroup v-for="g in nav" :key="g.label">
          <SidebarGroupLabel>{{ g.label }}</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              <SidebarMenuItem v-for="item in g.items" :key="item.to">
                <SidebarMenuButton as-child :is-active="item.match(route.path)" :tooltip="item.label">
                  <RouterLink :to="item.to">
                    <component :is="item.icon" />
                    <span>{{ item.label }}</span>
                  </RouterLink>
                </SidebarMenuButton>
              </SidebarMenuItem>
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>
      <SidebarFooter>
        <DropdownMenu>
          <DropdownMenuTrigger as-child>
            <SidebarMenuButton size="lg" class="data-[state=open]:bg-sidebar-accent">
              <Avatar class="size-8 rounded-lg">
                <AvatarFallback class="rounded-lg text-xs">{{ initials }}</AvatarFallback>
              </Avatar>
              <div class="grid flex-1 text-left text-sm leading-tight">
                <span class="truncate font-medium">{{ session.user?.firstname }} {{ session.user?.lastname }}</span>
                <span class="truncate text-xs text-muted-foreground">{{ session.user?.email }}</span>
              </div>
            </SidebarMenuButton>
          </DropdownMenuTrigger>
          <DropdownMenuContent side="top" align="start" class="w-56">
            <DropdownMenuLabel class="truncate">{{ session.user?.email }}</DropdownMenuLabel>
            <DropdownMenuSeparator />
            <DropdownMenuItem as-child><RouterLink to="/profile"><UserIcon /> Profile</RouterLink></DropdownMenuItem>
            <DropdownMenuItem as-child><RouterLink to="/credentials"><KeyRoundIcon /> Credentials</RouterLink></DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem @click="logout"><LogOutIcon /> Sign out</DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarFooter>
      <SidebarRail />
    </Sidebar>

    <SidebarInset class="h-svh overflow-hidden">
      <header class="relative flex h-12 shrink-0 items-center gap-2 border-b px-3">
        <SidebarTrigger />
        <Separator orientation="vertical" class="mr-1 h-4!" />
        <h1 class="text-sm font-medium">{{ route.meta.title }}</h1>
        <div class="ml-auto flex items-center gap-1">
          <ActivityIcon v-if="outstanding > 0" class="size-4 animate-pulse text-muted-foreground" />
          <ThemeToggle />
        </div>
        <div v-if="outstanding > 0" class="absolute inset-x-0 bottom-0 h-0.5 overflow-hidden">
          <div class="h-full w-1/3 animate-[progress_1.2s_ease-in-out_infinite] bg-primary" />
        </div>
      </header>
      <main class="min-h-0 flex-1 overflow-auto" @click="focusPageScroll">
        <RouterView />
      </main>
    </SidebarInset>
  </SidebarProvider>
</template>

<style>
@keyframes progress {
  0% { transform: translateX(-100%); }
  100% { transform: translateX(300%); }
}
</style>
