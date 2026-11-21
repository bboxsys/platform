import { describe, it, expect } from 'vitest'
import { useAsync, errorMessage } from './useAsync'
import { HttpError } from '../lib/http'

// A promise whose resolution we control from the test.
function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

describe('useAsync stale-result guard', () => {
  it('a superseded slow run neither overwrites data nor returns its value', async () => {
    const slow = deferred<string>()
    const fast = deferred<string>()
    const queue = [slow.promise, fast.promise]

    const { data, run } = useAsync<string>(() => queue.shift()!, 'initial')

    // Kick off A (slow) then B (fast). B is the newest run and should win.
    const aPromise = run()
    const bPromise = run()

    // B resolves first.
    fast.resolve('B')
    const bResult = await bPromise
    expect(bResult).toBe('B')
    expect(data.value).toBe('B')

    // Now the stale A resolves. It must NOT clobber data and must resolve
    // undefined to its own awaiter.
    slow.resolve('A')
    const aResult = await aPromise
    expect(aResult).toBeUndefined()
    expect(data.value).toBe('B')
  })

  it('the latest run wins and updates loading/error/data', async () => {
    const { data, loading, error, run } = useAsync<number>(async () => 42, 0)
    const result = await run()
    expect(result).toBe(42)
    expect(data.value).toBe(42)
    expect(loading.value).toBe(false)
    expect(error.value).toBe('')
  })

  it('records an error message when the fetcher rejects', async () => {
    const { error, run } = useAsync<number>(async () => {
      throw new Error('kaboom')
    }, 0)
    const result = await run()
    expect(result).toBeUndefined()
    expect(error.value).toBe('kaboom')
  })
})

describe('errorMessage', () => {
  it('prefers the error field of an HttpError JSON body', () => {
    expect(errorMessage(new HttpError(400, JSON.stringify({ error: 'nope' })))).toBe('nope')
  })

  it('falls back to the message field', () => {
    expect(errorMessage(new HttpError(400, JSON.stringify({ message: 'msg' })))).toBe('msg')
  })

  it('maps common status codes to friendly text', () => {
    expect(errorMessage(new HttpError(401, ''))).toContain('Sign in')
    expect(errorMessage(new HttpError(403, ''))).toContain('permission')
    expect(errorMessage(new HttpError(404, ''))).toBe('Not found.')
  })

  it('uses the trimmed body for other statuses', () => {
    expect(errorMessage(new HttpError(500, '  server exploded  '))).toBe('server exploded')
  })

  it('handles plain Error and non-error values', () => {
    expect(errorMessage(new Error('boom'))).toBe('boom')
    expect(errorMessage('a string')).toBe('a string')
  })
})
