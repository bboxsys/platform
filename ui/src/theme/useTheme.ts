// The theme mode -- light, dark, or following the system -- persisted per
// browser and applied as the `dark` class on <html>, which is what theme.css
// and the `dark:` variant key on.
//
// One shared state for the whole app, so the toggle in the header and a chart
// that reads isDark agree. The storage key is the one vueuse's useDark uses,
// so an app moving here from useDark keeps every user's saved preference.
import { computed, type WritableComputedRef } from 'vue'
import { createGlobalState, useColorMode } from '@vueuse/core'
import { themeEpoch } from './tokens'

export type ThemeMode = 'light' | 'dark' | 'system'

const useThemeState = createGlobalState(() => {
  const color = useColorMode({
    emitAuto: true,
    // No class for light: the stylesheet's :root is the light theme.
    modes: { light: '', dark: 'dark' },
  })

  const mode: WritableComputedRef<ThemeMode> = computed({
    get: () => (color.value === 'auto' ? 'system' : color.value),
    set: (value) => { color.value = value === 'system' ? 'auto' : value },
  })

  // The resolved appearance, whatever the mode. Setting it pins an explicit
  // mode unless that matches the system, in which case the preference goes
  // back to following the system -- the same rule as useDark, so a user who
  // toggles back to their OS setting is not left with a stale pin.
  const isDark: WritableComputedRef<boolean> = computed({
    get: () => color.state.value === 'dark',
    set: (dark) => {
      const wanted: ThemeMode = dark ? 'dark' : 'light'
      mode.value = color.system.value === wanted ? 'system' : wanted
    },
  })

  const toggle = (): void => { isDark.value = !isDark.value }

  return { mode, isDark, toggle, themeEpoch }
})

export function useTheme() {
  return useThemeState()
}
