<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { toast } from 'vue-sonner'
import { CopyIcon, PlusIcon, Trash2Icon } from '@lucide/vue'
import { oidcApi, usersApi } from '@/api/admin'
import { useAsync, errorMessage, HttpError } from '@bboxsys/ui'
import { ConfirmDialog } from '@bboxsys/ui'
import { adminBase } from '@/stores/session'
import { ROLES, type OIDCConnectionInput } from '@bboxsys/ui'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Checkbox } from '@/components/ui/checkbox'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

const props = defineProps<{ accountId: string }>()

const DEFAULT_SCOPES = 'openid email profile groups'

const form = reactive({
  slug: '',
  enabled: false,
  issuer_url: '',
  client_id: '',
  client_secret: '',
  redirect_uri: '',
  groups_claim: 'groups',
  auto_provision: true,
  default_roles: ['user'] as string[],
})
const scopesText = ref(DEFAULT_SCOPES)
const mappings = ref<{ group: string; role: string }[]>([])

const exists = ref(false)
const loading = ref(true)
const saving = ref(false)
const loadError = ref('')
const confirmDelete = ref(false)

// Available roles for mapping/default dropdowns; falls back to the built-in set.
const roles = useAsync(() => usersApi.roles(), ROLES as string[])
const roleList = computed(() => (roles.data.value?.length ? roles.data.value : (ROLES as string[])))

const authorizeUrl = computed(() => (form.slug.trim() ? `${adminBase()}/api/v1/oidc/${form.slug.trim()}/authorize` : ''))
const suggestedRedirect = computed(() => (form.slug.trim() ? `${adminBase()}/api/v1/oidc/${form.slug.trim()}/callback` : ''))

function resetForm() {
  form.slug = ''
  form.enabled = false
  form.issuer_url = ''
  form.client_id = ''
  form.client_secret = ''
  form.redirect_uri = ''
  form.groups_claim = 'groups'
  form.auto_provision = true
  form.default_roles = ['user']
  scopesText.value = DEFAULT_SCOPES
  mappings.value = []
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const c = await oidcApi.get(props.accountId)
    exists.value = true
    form.slug = c.slug
    form.enabled = c.enabled
    form.issuer_url = c.issuer_url
    form.client_id = c.client_id
    form.client_secret = '' // the server never returns the secret
    form.redirect_uri = c.redirect_uri
    form.groups_claim = c.groups_claim || 'groups'
    form.auto_provision = c.auto_provision
    form.default_roles = c.default_roles?.length ? [...c.default_roles] : ['user']
    scopesText.value = (c.scopes?.length ? c.scopes : DEFAULT_SCOPES.split(' ')).join(' ')
    mappings.value = Object.entries(c.group_role_mappings || {}).map(([group, role]) => ({ group, role }))
  } catch (e) {
    if (e instanceof HttpError && e.status === 404) {
      exists.value = false
      resetForm()
    } else {
      loadError.value = errorMessage(e)
    }
  } finally {
    loading.value = false
  }
}

onMounted(() => { load(); roles.run() })
watch(() => props.accountId, load)

function toggleDefaultRole(role: string, on: boolean) {
  const set = new Set(form.default_roles)
  if (on) set.add(role)
  else set.delete(role)
  form.default_roles = [...set]
}

function addMapping() {
  mappings.value.push({ group: '', role: roleList.value[0] ?? 'user' })
}
function removeMapping(i: number) {
  mappings.value.splice(i, 1)
}

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    toast.success('Copied to clipboard')
  } catch {
    toast.error('Could not copy')
  }
}

async function save() {
  saving.value = true
  try {
    const payload: OIDCConnectionInput = {
      slug: form.slug.trim(),
      enabled: form.enabled,
      issuer_url: form.issuer_url.trim(),
      client_id: form.client_id.trim(),
      redirect_uri: form.redirect_uri.trim(),
      scopes: scopesText.value.split(/[\s,]+/).map(s => s.trim()).filter(Boolean),
      groups_claim: form.groups_claim.trim() || 'groups',
      group_role_mappings: Object.fromEntries(
        mappings.value.filter(m => m.group.trim()).map(m => [m.group.trim(), m.role]),
      ),
      auto_provision: form.auto_provision,
      default_roles: form.default_roles.length ? form.default_roles : ['user'],
    }
    if (form.client_secret) payload.client_secret = form.client_secret
    await oidcApi.put(payload, props.accountId)
    toast.success('Identity provider saved')
    form.client_secret = ''
    exists.value = true
    load()
  } catch (e) {
    toast.error(errorMessage(e))
  } finally {
    saving.value = false
  }
}

// Throws on failure so ConfirmDialog surfaces the error via @error.
async function doDelete() {
  await oidcApi.remove(props.accountId)
  toast.success('Identity provider removed')
  exists.value = false
  resetForm()
}
</script>

<template>
  <div class="max-w-3xl">
    <Alert v-if="loadError" variant="destructive" class="mb-4"><AlertDescription>{{ loadError }}</AlertDescription></Alert>

    <form v-if="!loading" class="flex flex-col gap-6" @submit.prevent="save">
      <!-- Provider -->
      <Card>
        <CardHeader>
          <div class="flex items-center justify-between gap-4">
            <div>
              <CardTitle>Single sign-on (OIDC)</CardTitle>
              <CardDescription>
                Let this account's users sign in through your identity provider. Roles are re-synced from
                the provider on every login.
              </CardDescription>
            </div>
            <label class="flex shrink-0 items-center gap-2 text-sm"><Switch v-model="form.enabled" /> Enabled</label>
          </div>
        </CardHeader>
        <CardContent class="grid gap-4">
          <div class="grid gap-1.5">
            <Label for="slug">Login slug</Label>
            <Input id="slug" v-model="form.slug" placeholder="acme" required />
            <p class="text-xs text-muted-foreground">Unique per-account. Used in the login URL below.</p>
          </div>
          <div class="grid gap-1.5">
            <Label for="issuer">Issuer URL</Label>
            <Input id="issuer" v-model="form.issuer_url" type="url" placeholder="https://idp.example.com" required />
            <p class="text-xs text-muted-foreground">The provider's OIDC discovery base (must serve /.well-known/openid-configuration).</p>
          </div>
          <div class="grid gap-4 sm:grid-cols-2">
            <div class="grid gap-1.5">
              <Label for="clientId">Client ID</Label>
              <Input id="clientId" v-model="form.client_id" required />
            </div>
            <div class="grid gap-1.5">
              <Label for="clientSecret">Client secret</Label>
              <Input id="clientSecret" v-model="form.client_secret" type="password" autocomplete="new-password"
                     :placeholder="exists ? 'Leave blank to keep current' : ''" :required="!exists" />
            </div>
          </div>
          <div class="grid gap-1.5">
            <Label for="redirect">Redirect URI</Label>
            <Input id="redirect" v-model="form.redirect_uri" type="url" placeholder="https://admin.example.com/api/v1/oidc/acme/callback" required />
            <p v-if="suggestedRedirect" class="flex items-center gap-1 text-xs text-muted-foreground">
              Suggested: <code class="rounded bg-muted px-1">{{ suggestedRedirect }}</code>
              <button type="button" class="hover:text-foreground" @click="form.redirect_uri = suggestedRedirect">use</button>
            </p>
          </div>
        </CardContent>
      </Card>

      <!-- Scopes & groups -->
      <Card>
        <CardHeader>
          <CardTitle class="text-base">Scopes &amp; provisioning</CardTitle>
        </CardHeader>
        <CardContent class="grid gap-4">
          <div class="grid gap-4 sm:grid-cols-2">
            <div class="grid gap-1.5">
              <Label for="scopes">Scopes</Label>
              <Input id="scopes" v-model="scopesText" placeholder="openid email profile groups" />
              <p class="text-xs text-muted-foreground">Space-separated.</p>
            </div>
            <div class="grid gap-1.5">
              <Label for="groupsClaim">Groups claim</Label>
              <Input id="groupsClaim" v-model="form.groups_claim" placeholder="groups" />
              <p class="text-xs text-muted-foreground">ID-token claim carrying group membership.</p>
            </div>
          </div>
          <label class="flex items-center gap-2 text-sm">
            <Switch v-model="form.auto_provision" /> Auto-provision users on first login
          </label>
          <div class="grid gap-1.5">
            <Label>Default roles</Label>
            <p class="text-xs text-muted-foreground">Applied when no group mapping matches.</p>
            <div class="flex flex-wrap gap-4 pt-1">
              <label v-for="r in roleList" :key="r" class="flex items-center gap-2 text-sm">
                <Checkbox :model-value="form.default_roles.includes(r)" @update:model-value="v => toggleDefaultRole(r, !!v)" />
                {{ r }}
              </label>
            </div>
          </div>
        </CardContent>
      </Card>

      <!-- Group -> role mappings -->
      <Card>
        <CardHeader>
          <div class="flex items-center justify-between">
            <div>
              <CardTitle class="text-base">Group &rarr; role mappings</CardTitle>
              <CardDescription>Map identity-provider groups to roles. The provider is authoritative.</CardDescription>
            </div>
            <Button type="button" variant="outline" size="sm" @click="addMapping"><PlusIcon /> Add</Button>
          </div>
        </CardHeader>
        <CardContent>
          <p v-if="mappings.length === 0" class="text-sm text-muted-foreground">No mappings. Users get the default roles above.</p>
          <div v-for="(m, i) in mappings" :key="i" class="mb-2 flex items-center gap-2">
            <Input v-model="m.group" placeholder="IdP group name" class="flex-1" />
            <span class="text-muted-foreground">&rarr;</span>
            <Select v-model="m.role">
              <SelectTrigger class="w-40"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem v-for="r in roleList" :key="r" :value="r">{{ r }}</SelectItem>
              </SelectContent>
            </Select>
            <Button type="button" variant="ghost" size="icon-sm" aria-label="Remove" @click="removeMapping(i)"><Trash2Icon /></Button>
          </div>
        </CardContent>
      </Card>

      <!-- Login URL -->
      <Card v-if="authorizeUrl">
        <CardHeader><CardTitle class="text-base">Login URL</CardTitle></CardHeader>
        <CardContent class="flex items-center gap-2">
          <code class="flex-1 overflow-x-auto rounded bg-muted px-2 py-1 text-xs">{{ authorizeUrl }}</code>
          <Button type="button" variant="ghost" size="icon-sm" aria-label="Copy" @click="copy(authorizeUrl)"><CopyIcon /></Button>
        </CardContent>
      </Card>

      <div class="flex items-center gap-2">
        <Button type="submit" :disabled="saving">{{ saving ? 'Saving...' : 'Save' }}</Button>
        <Button v-if="exists" type="button" variant="outline" class="text-destructive" @click="confirmDelete = true">Remove</Button>
      </div>
    </form>

    <ConfirmDialog
      v-model:open="confirmDelete"
      title="Remove identity provider?"
      description="Users will no longer be able to sign in through this provider. Local accounts are unaffected."
      confirm-text="Remove"
      destructive
      :action="doDelete"
      @error="m => toast.error(m)"
    />
  </div>
</template>
