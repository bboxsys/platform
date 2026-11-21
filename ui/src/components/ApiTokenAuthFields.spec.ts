import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick, ref } from 'vue'
import ApiTokenAuthFields from './ApiTokenAuthFields.vue'
import { emptyAPITokenAuthForm } from '../lib/apitokenAuth'

describe('ApiTokenAuthFields', () => {
  it('shows the bearer field for a static token and the OIDC block otherwise', async () => {
    const form = ref(emptyAPITokenAuthForm())
    const wrapper = mount(ApiTokenAuthFields, {
      props: { 'modelValue': form.value, 'onUpdate:modelValue': (v) => { form.value = v } },
    })
    expect(wrapper.find('[data-slot="apitoken-auth-static"]').exists()).toBe(true)
    expect(wrapper.find('[data-slot="apitoken-auth-oidc"]').exists()).toBe(false)

    form.value.authMode = 'client_credentials'
    await nextTick()
    expect(wrapper.find('[data-slot="apitoken-auth-static"]').exists()).toBe(false)
    expect(wrapper.find('[data-slot="apitoken-auth-oidc"]').exists()).toBe(true)
    expect(wrapper.find('#apitoken-auth-issuer').exists()).toBe(true)
    expect(wrapper.find('#apitoken-auth-username').exists()).toBe(false)
    expect(wrapper.find('[data-slot="apitoken-auth-password-notice"]').exists()).toBe(false)

    form.value.authMode = 'password'
    await nextTick()
    expect(wrapper.find('#apitoken-auth-username').exists()).toBe(true)
    expect(wrapper.find('#apitoken-auth-password').exists()).toBe(true)
    expect(wrapper.find('[data-slot="apitoken-auth-password-notice"]').exists()).toBe(true)

    form.value.authMode = 'refresh_token'
    await nextTick()
    expect(wrapper.find('#apitoken-auth-refresh-token').exists()).toBe(true)
    expect(wrapper.find('#apitoken-auth-username').exists()).toBe(false)
  })

  it('writes typed values back into the form', async () => {
    const form = ref(emptyAPITokenAuthForm({ authMode: 'client_credentials' }))
    const wrapper = mount(ApiTokenAuthFields, {
      props: { 'modelValue': form.value, 'onUpdate:modelValue': (v) => { form.value = v } },
    })
    await wrapper.find('#apitoken-auth-issuer').setValue('https://idp.example.com/realm')
    await wrapper.find('#apitoken-auth-client-id').setValue('client')
    await wrapper.find('#apitoken-auth-client-secret').setValue('s3cret')
    expect(form.value.oidcIssuer).toBe('https://idp.example.com/realm')
    expect(form.value.oidcClientId).toBe('client')
    expect(form.value.oidcClientSecret).toBe('s3cret')
  })

  it('names the audience, prefixes ids, and relaxes required secrets when editing', () => {
    const form = ref(emptyAPITokenAuthForm({ authMode: 'client_credentials' }))
    const wrapper = mount(ApiTokenAuthFields, {
      props: { modelValue: form.value, editing: true, audience: 'Example Cloud', idPrefix: 'ex' },
    })
    expect(wrapper.text()).toContain('Token sent to Example Cloud')
    expect(wrapper.text()).toContain('Entering any secret replaces the sealed OIDC secret set')
    const secret = wrapper.find('#ex-client-secret')
    expect(secret.exists()).toBe(true)
    expect(secret.attributes('placeholder')).toBe('Leave blank to keep stored secrets')
  })
})
