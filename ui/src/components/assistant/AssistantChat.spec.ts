import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import AssistantChat from './AssistantChat.vue'
import type { AssistantApi } from '../../lib/assistantApi'
import type { AIEvent, AISession, AISessionSummary, AIStatus } from '../../types/admin'

const usage = { input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 }

function fakeApi(status: AIStatus, sessions: AISessionSummary[] = []): { api: AssistantApi; sent: string[] } {
  const sent: string[] = []
  const store = new Map<string, AISession>()
  for (const s of sessions) {
    store.set(s.id, { ...s, account_id: 'A', user_id: 'U', messages: [], usage })
  }
  const api: AssistantApi = {
    service: 'things',
    status: async () => status,
    sessions: {
      list: async () => [...store.values()].map(s => ({ id: s.id, title: s.title, provider: s.provider, model: s.model, message_count: s.messages.length, usage: s.usage, created_at: s.created_at, updated_at: s.updated_at })),
      create: async input => {
        const s: AISession = { id: `s${store.size + 1}`, account_id: 'A', user_id: 'U', title: input?.title, provider: 'fake', model: 'fake-1', messages: [], usage, created_at: 'now', updated_at: 'now' }
        store.set(s.id, s)
        return s
      },
      // Fresh objects, as a JSON response is: the component must not rely
      // on the store's identity.
      get: async id => {
        const s = store.get(id)
        if (!s) throw new Error('missing')
        return structuredClone(s)
      },
      rename: async (id, title) => {
        const s = store.get(id)!
        s.title = title
        return { id, title, provider: s.provider, model: s.model, message_count: s.messages.length, usage, created_at: 'now', updated_at: 'now' }
      },
      remove: async id => { store.delete(id) },
    },
    sendMessage: async (id, content, onEvent: (e: AIEvent) => void) => {
      sent.push(content)
      onEvent({ type: 'tool_use', call: { id: 'c1', name: 'things_list', input: { q: 1 } } })
      onEvent({ type: 'tool_result', result: { id: 'c1', name: 'things_list', result: { content: '[]' } } })
      onEvent({ type: 'text', text: 'Three ' })
      onEvent({ type: 'text', text: 'things.' })
      onEvent({ type: 'usage', usage: { input_tokens: 10, output_tokens: 5, cache_read_tokens: 0, cache_write_tokens: 0 } })
      onEvent({ type: 'done', stop_reason: 'end_turn' })
      const s = store.get(id)!
      s.messages = [
        { role: 'user', content: null, text: content, created_at: 'now' },
        { role: 'assistant', content: null, text: 'Three things.', tool_calls: [{ id: 'c1', name: 'things_list', input: { q: 1 } }], created_at: 'now' },
      ]
      s.usage = { input_tokens: 10, output_tokens: 5, cache_read_tokens: 0, cache_write_tokens: 0 }
    },
  }
  return { api, sent }
}

const enabled: AIStatus = {
  enabled: true, model: { name: 'claude', provider: 'anthropic', model: 'claude-test' },
  models: [{ name: 'claude', provider: 'anthropic', model: 'claude-test' }], embedding: null, tools: ['things_list'], mcp_servers: [], allow_writes: false,
}

describe('AssistantChat', () => {
  it('points at the credentials when no model is configured', async () => {
    const { api } = fakeApi({ ...enabled, enabled: false, model: null, models: [] })
    const wrapper = mount(AssistantChat, { props: { api, layout: 'panel' }, slots: { unconfigured: 'Add a key first.' } })
    await flushPromises()
    expect(wrapper.text()).toContain('Add a key first.')
    expect(wrapper.find('textarea').attributes('disabled')).toBeDefined()
  })

  it('opens the newest session, sends a message and shows the streamed run', async () => {
    const { api, sent } = fakeApi(enabled, [
      { id: 'old', title: 'Older', provider: 'fake', model: 'fake-1', message_count: 0, usage, created_at: 'a', updated_at: 'a' },
    ])
    const busy: boolean[] = []
    const wrapper = mount(AssistantChat, {
      props: { api, layout: 'page', 'onUpdate:busy': (v: boolean) => busy.push(v) },
    })
    await flushPromises()
    expect(wrapper.find('[data-slot="assistant-sessions"]').text()).toContain('Older')
    expect(busy.at(-1)).toBe(false)

    await wrapper.find('textarea').setValue('How many things?')
    await wrapper.find('textarea').trigger('keydown.enter')
    await flushPromises()
    expect(sent).toEqual(['How many things?'])
    const transcript = wrapper.find('[data-slot="assistant-transcript"]').text()
    expect(transcript).toContain('How many things?')
    expect(transcript).toContain('Three things.')
    expect(transcript).toContain('Called things_list')
    expect(wrapper.find('[data-slot="assistant-composer"]').text()).toContain('10 in')
    // The session without a title was named after the first message.
    expect(wrapper.find('[data-slot="assistant-sessions"]').text()).toContain('Older')
    expect(busy).toContain(true)
    expect(busy.at(-1)).toBe(false)
  })

  it('keeps the sessions in a dropdown in the panel layout', async () => {
    const { api } = fakeApi(enabled, [
      { id: 'one', title: 'First', provider: 'fake', model: 'fake-1', message_count: 2, usage, created_at: 'a', updated_at: 'a' },
    ])
    const wrapper = mount(AssistantChat, { props: { api, layout: 'panel', serviceLabel: 'Things' } })
    await flushPromises()
    expect(wrapper.find('[data-slot="assistant-sessions"]').exists()).toBe(false)
    expect(wrapper.find('[data-slot="assistant-sessions-bar"]').exists()).toBe(true)
    expect(wrapper.find('textarea').attributes('placeholder')).toContain('Things assistant')
  })
})

vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
