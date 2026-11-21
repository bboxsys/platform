import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import ObjectAction from './ObjectAction.vue'

describe('ObjectAction', () => {
  it('names the button by its action', () => {
    const wrapper = mount(ObjectAction, { props: { action: 'refresh' } })
    expect(wrapper.find('button').attributes('aria-label')).toBe('Refresh')
  })

  it('lets a view name the action itself', () => {
    const wrapper = mount(ObjectAction, { props: { action: 'create', label: 'New VPC' } })
    expect(wrapper.find('button').attributes('aria-label')).toBe('New VPC')
    expect(wrapper.find('span').attributes('title')).toBe('New VPC')
  })

  it('emits click', async () => {
    const wrapper = mount(ObjectAction, { props: { action: 'edit' } })
    await wrapper.find('button').trigger('click')
    expect(wrapper.emitted('click')).toHaveLength(1)
  })

  it('is disabled and busy while loading', () => {
    const wrapper = mount(ObjectAction, { props: { action: 'refresh', loading: true } })
    const button = wrapper.find('button')
    expect(button.attributes('disabled')).toBeDefined()
    expect(button.attributes('aria-busy')).toBe('true')
    expect(wrapper.find('svg').classes()).toContain('animate-spin')
  })

  it('marks delete in the destructive color', () => {
    const wrapper = mount(ObjectAction, { props: { action: 'delete' } })
    expect(wrapper.find('svg').classes()).toContain('text-destructive')
  })
})
