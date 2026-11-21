<script setup lang="ts">
import { computed } from 'vue'
import type { APITokenAuthMode } from '../types/admin'
import { API_TOKEN_AUTH_MODES, OIDC_CLIENT_AUTH_METHODS, OIDC_TOKEN_FIELDS } from '../types/admin'
import type { APITokenAuthForm } from '../lib/apitokenAuth'
import {
  API_TOKEN_AUTH_MODE_LABELS, OIDC_CLIENT_AUTH_METHOD_LABELS, OIDC_TOKEN_FIELD_LABELS,
} from '../lib/apitokenAuth'
import { Alert, AlertDescription } from '../ui/alert'
import { Checkbox } from '../ui/checkbox'
import { Input } from '../ui/input'
import { Label } from '../ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'

// ApiTokenAuthFields is the authentication section of an API credential
// form: the mode selector, the bearer field for a static token, and the OIDC
// inputs the backend mints from otherwise. It binds to an APITokenAuthForm
// (emptyAPITokenAuthForm / apiTokenAuthForm) and leaves the rest of the form
// (name, scope, endpoint, resource association) to the product view, which
// validates the whole thing with validateAPITokenAuth before writing.
//
// Secret fields are always blank: the server never returns a stored secret,
// and on an edit a blank secret keeps the stored one.
const props = withDefaults(defineProps<{
  // True when editing a stored credential: secrets become optional and the
  // placeholders say that leaving them blank keeps what is stored.
  editing?: boolean
  // What the minted token is sent to, for the "Token sent to ..." label.
  audience?: string
  // The modes offered, in order. Defaults to all of them.
  modes?: APITokenAuthMode[]
  // Prefix for element ids, so two instances on one page do not collide.
  idPrefix?: string
  disabled?: boolean
}>(), {
  editing: false,
  audience: 'the API',
  modes: () => API_TOKEN_AUTH_MODES,
  idPrefix: 'apitoken-auth',
  disabled: false,
})

const model = defineModel<APITokenAuthForm>({ required: true })

const id = (suffix: string) => `${props.idPrefix}-${suffix}`
const oidc = computed(() => model.value.authMode !== 'static')
const secretPlaceholder = computed(() => (props.editing ? 'Leave blank to keep the stored secret' : ''))
</script>

<template>
  <div class="grid gap-4" data-slot="apitoken-auth">
    <div class="grid gap-1.5">
      <Label :for="id('mode')">Authentication</Label>
      <Select v-model="model.authMode" :disabled="disabled">
        <SelectTrigger :id="id('mode')" data-slot="apitoken-auth-mode"><SelectValue /></SelectTrigger>
        <SelectContent>
          <SelectItem v-for="mode in modes" :key="mode" :value="mode">{{ API_TOKEN_AUTH_MODE_LABELS[mode] }}</SelectItem>
        </SelectContent>
      </Select>
      <p class="text-xs text-muted-foreground">
        {{ oidc
          ? 'The backend discovers the token endpoint from the issuer, mints a token on demand, and renews it before it expires.'
          : 'Use an already-minted bearer until it expires.' }}
      </p>
    </div>

    <div v-if="!oidc" class="grid gap-1.5" data-slot="apitoken-auth-static">
      <Label :for="id('token')">Access token</Label>
      <Input
        :id="id('token')"
        v-model="model.token"
        type="password"
        autocomplete="new-password"
        :required="!editing"
        :disabled="disabled"
        :placeholder="editing ? 'Leave blank to keep the stored token' : 'Bearer token'"
      />
    </div>

    <div v-else class="grid gap-4 rounded-lg border bg-muted/20 p-3" data-slot="apitoken-auth-oidc">
      <div class="grid gap-1.5">
        <Label :for="id('issuer')">OIDC issuer</Label>
        <Input :id="id('issuer')" v-model="model.oidcIssuer" required autocomplete="off" :disabled="disabled" placeholder="https://idp.example.com/realms/example" />
        <p class="text-xs text-muted-foreground">
          The backend reads <code>/.well-known/openid-configuration</code> here and uses the advertised token endpoint.
        </p>
      </div>
      <div class="grid gap-4 sm:grid-cols-2">
        <div class="grid gap-1.5">
          <Label :for="id('client-id')">Client ID</Label>
          <Input :id="id('client-id')" v-model="model.oidcClientId" required autocomplete="off" :disabled="disabled" />
        </div>
        <div class="grid gap-1.5">
          <Label :for="id('client-secret')">Client secret</Label>
          <Input
            :id="id('client-secret')"
            v-model="model.oidcClientSecret"
            type="password"
            autocomplete="new-password"
            :disabled="disabled"
            :placeholder="editing ? 'Leave blank to keep stored secrets' : model.authMode === 'client_credentials' ? 'Required' : 'Optional for public clients'"
          />
        </div>
      </div>

      <template v-if="model.authMode === 'password'">
        <Alert data-slot="apitoken-auth-password-notice">
          <AlertDescription>
            The password grant is a legacy provider compatibility mode. Prefer client credentials or a refresh token when available.
          </AlertDescription>
        </Alert>
        <div class="grid gap-4 sm:grid-cols-2">
          <div class="grid gap-1.5">
            <Label :for="id('username')">Username</Label>
            <Input :id="id('username')" v-model="model.oidcUsername" required autocomplete="off" :disabled="disabled" />
          </div>
          <div class="grid gap-1.5">
            <Label :for="id('password')">Password</Label>
            <Input
              :id="id('password')"
              v-model="model.oidcPassword"
              type="password"
              autocomplete="new-password"
              :required="!editing"
              :disabled="disabled"
              :placeholder="secretPlaceholder"
            />
          </div>
        </div>
      </template>

      <div v-if="model.authMode === 'refresh_token'" class="grid gap-1.5">
        <Label :for="id('refresh-token')">Refresh token</Label>
        <Input
          :id="id('refresh-token')"
          v-model="model.oidcRefreshToken"
          type="password"
          autocomplete="new-password"
          :required="!editing"
          :disabled="disabled"
          :placeholder="editing ? 'Leave blank to keep the stored refresh token' : ''"
        />
        <p class="text-xs text-muted-foreground">A refresh token the provider rotates is sealed back into the credential automatically.</p>
      </div>

      <div class="grid gap-4 sm:grid-cols-2">
        <div class="grid gap-1.5">
          <Label :for="id('scopes')">Scopes</Label>
          <Input :id="id('scopes')" v-model="model.oidcScopes" autocomplete="off" :disabled="disabled" placeholder="openid email profile" />
        </div>
        <div class="grid gap-1.5">
          <Label :for="id('token-field')">Token sent to {{ audience }}</Label>
          <Select v-model="model.oidcTokenField" :disabled="disabled">
            <SelectTrigger :id="id('token-field')"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem v-for="field in OIDC_TOKEN_FIELDS" :key="field" :value="field">{{ OIDC_TOKEN_FIELD_LABELS[field] }}</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>

      <div class="grid gap-1.5">
        <Label :for="id('client-auth')">Token endpoint authentication</Label>
        <Select v-model="model.oidcClientAuthMethod" :disabled="disabled">
          <SelectTrigger :id="id('client-auth')"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem v-for="method in OIDC_CLIENT_AUTH_METHODS" :key="method" :value="method">{{ OIDC_CLIENT_AUTH_METHOD_LABELS[method] }}</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <Label class="flex items-start gap-3 rounded-lg border p-3 font-normal">
        <Checkbox v-model="model.oidcInsecureSkipVerify" class="mt-0.5" :disabled="disabled" data-slot="apitoken-auth-oidc-insecure" />
        <span>
          <span class="text-sm font-medium">Skip OIDC TLS verification</span>
          <span class="mt-0.5 block text-xs text-muted-foreground">
            Applies only to discovery and token minting against a provider with a self-signed certificate, not to {{ audience }}.
          </span>
        </span>
      </Label>

      <p v-if="editing" class="text-xs text-muted-foreground">
        Entering any secret replaces the sealed OIDC secret set. Re-enter every secret the mode needs when rotating one.
      </p>
    </div>
  </div>
</template>
