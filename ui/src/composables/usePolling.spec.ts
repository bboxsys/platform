import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import { effectScope } from 'vue'
import { usePolling } from './usePolling'

describe('usePolling', () => {
  beforeEach(() => { vi.useFakeTimers() })
  afterEach(() => { vi.useRealTimers() })

  it('calls the function on every interval until paused', () => {
    const fn = vi.fn()
    const scope = effectScope()
    const polling = scope.run(() => usePolling(fn, 1000))!
    vi.advanceTimersByTime(3000)
    expect(fn).toHaveBeenCalledTimes(3)
    polling.pause()
    vi.advanceTimersByTime(3000)
    expect(fn).toHaveBeenCalledTimes(3)
    polling.resume()
    vi.advanceTimersByTime(1000)
    expect(fn).toHaveBeenCalledTimes(4)
    scope.stop()
  })

  it('stops with the scope that started it', () => {
    const fn = vi.fn()
    const scope = effectScope()
    scope.run(() => usePolling(fn, 1000))
    scope.stop()
    vi.advanceTimersByTime(3000)
    expect(fn).not.toHaveBeenCalled()
  })

  it('does not start when immediate is false', () => {
    const fn = vi.fn()
    const scope = effectScope()
    const polling = scope.run(() => usePolling(fn, 1000, false))!
    vi.advanceTimersByTime(3000)
    expect(fn).not.toHaveBeenCalled()
    expect(polling.active.value).toBe(false)
    scope.stop()
  })
})
