import { describe, it, expect, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { reactive } from 'vue'
import LoginCard from './LoginCard.vue'
import { HttpError } from '../lib/http'
import type { SessionStore } from '../session/createSessionStore'

// A minimal stand-in for the Pinia store: only what LoginCard touches.
function fakeSession(loginImpl: (email: string, password: string) => Promise<void>, loginUrl = 'https://admin.example:8901') {
  return reactive({
    endpoint: (kind: string) => (kind === 'login_service' ? loginUrl : ''),
    bootstrapEndpoints: vi.fn().mockResolvedValue(undefined),
    login: vi.fn(loginImpl),
  }) as unknown as SessionStore
}

async function fillAndSubmit(wrapper: ReturnType<typeof mount>) {
  await wrapper.find('#email').setValue('  me@example.com ')
  await wrapper.find('#password').setValue('pw')
  await wrapper.find('form').trigger('submit')
  await flushPromises()
}

describe('LoginCard', () => {
  it('bootstraps endpoints, signs in with a trimmed email, and emits success', async () => {
    const session = fakeSession(async () => {})
    const wrapper = mount(LoginCard, { props: { session, title: 'My App', description: 'Sign in' } })
    expect(session.bootstrapEndpoints).toHaveBeenCalled()
    expect(wrapper.text()).toContain('My App')
    expect(wrapper.text()).toContain('Sign in')

    await fillAndSubmit(wrapper)
    expect(session.login).toHaveBeenCalledWith('me@example.com', 'pw')
    expect(wrapper.emitted('success')).toHaveLength(1)
  })

  it('shows the login service URL under the form', () => {
    const wrapper = mount(LoginCard, { props: { session: fakeSession(async () => {}), title: 'T' } })
    expect(wrapper.find('[data-slot="login-service"]').text()).toBe('https://admin.example:8901')
  })

  it('hides the footer when no login service is known', () => {
    const wrapper = mount(LoginCard, { props: { session: fakeSession(async () => {}, ''), title: 'T' } })
    expect(wrapper.find('[data-slot="login-service"]').exists()).toBe(false)
  })

  it('maps 401 and 403 to friendly messages and passes other errors through', async () => {
    for (const [err, msg] of [
      [new HttpError(401, ''), 'Email or password is incorrect.'],
      [new HttpError(403, ''), 'This account is locked. Ask an administrator to unlock it.'],
      [new Error('Login service URL is not available.'), 'Login service URL is not available.'],
    ] as const) {
      const wrapper = mount(LoginCard, { props: { session: fakeSession(async () => { throw err }), title: 'T' } })
      await fillAndSubmit(wrapper)
      expect(wrapper.find('[data-slot="login-error"]').text()).toBe(msg)
      expect(wrapper.emitted('success')).toBeUndefined()
    }
  })

  it('offers the OAuth button only when enabled and emits oauth', async () => {
    const session = fakeSession(async () => {})
    const off = mount(LoginCard, { props: { session, title: 'T' } })
    expect(off.text()).not.toContain('Continue with Google')

    const on = mount(LoginCard, { props: { session, title: 'T', oauthEnabled: true, oauthLabel: 'Continue with Acme' } })
    const btn = on.findAll('button').find(b => b.text() === 'Continue with Acme')
    expect(btn).toBeDefined()
    await btn!.trigger('click')
    expect(on.emitted('oauth')).toHaveLength(1)
  })

  it('renders a logo image or the logo slot', () => {
    const session = fakeSession(async () => {})
    const img = mount(LoginCard, { props: { session, title: 'T', logo: '/logo.svg', logoAlt: 'Mark' } })
    expect(img.find('img').attributes('src')).toBe('/logo.svg')
    expect(img.find('img').attributes('alt')).toBe('Mark')

    const slot = mount(LoginCard, { props: { session, title: 'T' }, slots: { logo: '<svg data-test="icon" />' } })
    expect(slot.find('[data-test="icon"]').exists()).toBe(true)
    expect(slot.find('img').exists()).toBe(false)
  })
})
