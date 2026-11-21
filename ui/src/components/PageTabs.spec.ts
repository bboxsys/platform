import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import PageTabs from './PageTabs.vue'

const tabs = [
  { value: 'a', label: 'Alpha' },
  { value: 'b', label: 'Beta', count: 3 },
  { value: 'c', label: 'Gamma', disabled: true },
  { value: 'd', label: 'Delta', count: 0 },
]

describe('PageTabs', () => {
  it('marks the selected value tab and underlines it in the brand color', () => {
    const wrapper = mount(PageTabs, { props: { tabs, modelValue: 'b', label: 'Kinds' } })
    const buttons = wrapper.findAll('[role=tab]')
    expect(buttons).toHaveLength(4)
    expect(wrapper.find('[role=tablist]').attributes('aria-label')).toBe('Kinds')
    expect(buttons[1].attributes('aria-selected')).toBe('true')
    expect(buttons[0].attributes('aria-selected')).toBe('false')
    expect(buttons[1].classes()).toContain('aria-selected:border-primary')
    expect(buttons[1].attributes('tabindex')).toBe('0')
    expect(buttons[0].attributes('tabindex')).toBe('-1')
  })

  it('shows a count only when it is positive', () => {
    const wrapper = mount(PageTabs, { props: { tabs, modelValue: 'a' } })
    const buttons = wrapper.findAll('[role=tab]')
    expect(buttons[1].text()).toBe('Beta 3')
    expect(buttons[3].text()).toBe('Delta')
  })

  it('emits the value on click and skips disabled tabs with the arrow keys', async () => {
    const wrapper = mount(PageTabs, { props: { tabs, modelValue: 'b' } })
    const buttons = wrapper.findAll('[role=tab]')
    await buttons[0].trigger('click')
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual(['a'])
    await buttons[1].trigger('keydown', { key: 'ArrowRight' })
    expect(wrapper.emitted('update:modelValue')?.[1]).toEqual(['d'])
    await buttons[0].trigger('keydown', { key: 'ArrowLeft' })
    expect(wrapper.emitted('update:modelValue')?.[2]).toEqual(['d'])
  })

  it('renders route tabs as links when the app has no router', () => {
    const wrapper = mount(PageTabs, { props: { tabs: [{ to: '/x/users', label: 'Users' }, { to: '/x/agents', label: 'Agents' }] } })
    const links = wrapper.findAll('nav a')
    expect(links.map(l => l.attributes('href'))).toEqual(['/x/users', '/x/agents'])
    expect(wrapper.find('[role=tablist]').exists()).toBe(false)
  })

  it('puts the actions slot at the right end of the bar', () => {
    const wrapper = mount(PageTabs, { props: { tabs, modelValue: 'a' }, slots: { actions: '<button class="act">Add</button>' } })
    expect(wrapper.find('.ml-auto .act').exists()).toBe(true)
  })
})
