import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { useTheme } from './useTheme'

// happy-dom does not expose localStorage; vueuse reads it from window, so the
// Map-backed stand-in has to be installed there before the shared state is
// first created.
function memoryStorage(): Storage {
  const m = new Map<string, string>()
  return {
    get length() { return m.size },
    clear: () => m.clear(),
    getItem: (k: string) => m.get(k) ?? null,
    key: (i: number) => [...m.keys()][i] ?? null,
    removeItem: (k: string) => { m.delete(k) },
    setItem: (k: string, v: string) => { m.set(k, v) },
  }
}

const storage = memoryStorage()

describe('useTheme', () => {
  beforeAll(() => {
    vi.stubGlobal('localStorage', storage)
    Object.defineProperty(window, 'localStorage', { value: storage, configurable: true })
  })

  beforeEach(() => {
    storage.clear()
  })

  it('is one shared state', () => {
    const a = useTheme()
    const b = useTheme()
    expect(a.isDark).toBe(b.isDark)
    expect(a.mode).toBe(b.mode)
  })

  it('pins an explicit mode and writes the dark class', async () => {
    const theme = useTheme()
    theme.mode.value = 'dark'
    expect(theme.isDark.value).toBe(true)
    await Promise.resolve()
    expect(document.documentElement.classList.contains('dark')).toBe(true)

    theme.mode.value = 'light'
    expect(theme.isDark.value).toBe(false)
    await Promise.resolve()
    expect(document.documentElement.classList.contains('dark')).toBe(false)
  })

  it('persists under the key useDark used', async () => {
    const theme = useTheme()
    theme.mode.value = 'dark'
    await nextTick()
    expect(storage.getItem('vueuse-color-scheme')).toBe('dark')
    theme.mode.value = 'system'
    await nextTick()
    expect(storage.getItem('vueuse-color-scheme')).toBe('auto')
  })

  it('toggle flips the resolved appearance', () => {
    const theme = useTheme()
    theme.mode.value = 'light'
    theme.toggle()
    expect(theme.isDark.value).toBe(true)
    theme.toggle()
    expect(theme.isDark.value).toBe(false)
  })
})
