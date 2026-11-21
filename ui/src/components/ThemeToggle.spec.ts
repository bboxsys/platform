import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import ThemeToggle from './ThemeToggle.vue'
import { useTheme } from '../theme/useTheme'

describe('ThemeToggle', () => {
  it('toggles the shared theme', async () => {
    const theme = useTheme()
    theme.mode.value = 'light'
    const wrapper = mount(ThemeToggle)
    expect(wrapper.attributes('aria-label')).toBe('Toggle theme')
    await wrapper.trigger('click')
    expect(theme.isDark.value).toBe(true)
    await wrapper.trigger('click')
    expect(theme.isDark.value).toBe(false)
  })
})
