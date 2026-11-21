import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import ClearableSearchInput from './ClearableSearchInput.vue'

describe('ClearableSearchInput', () => {
  it('shows no clear button while empty', () => {
    const wrapper = mount(ClearableSearchInput, { props: { modelValue: '' } })
    expect(wrapper.find('[data-slot="search-clear"]').exists()).toBe(false)
  })

  it('emits the typed value', async () => {
    const wrapper = mount(ClearableSearchInput, { props: { modelValue: '' } })
    await wrapper.find('input').setValue('web')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual(['web'])
  })

  it('clears to an empty string', async () => {
    const wrapper = mount(ClearableSearchInput, { props: { modelValue: 'web', clearLabel: 'Clear filter' } })
    const clear = wrapper.find('[data-slot="search-clear"]')
    expect(clear.attributes('aria-label')).toBe('Clear filter')
    await clear.trigger('click')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([''])
  })

  it('passes the label and placeholder to the input', () => {
    const wrapper = mount(ClearableSearchInput, {
      props: { modelValue: '', ariaLabel: 'Filter routes', placeholder: 'Filter routes...' },
    })
    const input = wrapper.find('input')
    expect(input.attributes('aria-label')).toBe('Filter routes')
    expect(input.attributes('placeholder')).toBe('Filter routes...')
  })
})
