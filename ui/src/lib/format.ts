// Display is imperial by default (matches geo package conventions).
const num = new Intl.NumberFormat('en-US')
const dateTime = new Intl.DateTimeFormat('en-US', { dateStyle: 'medium', timeStyle: 'short' })
const dateOnly = new Intl.DateTimeFormat('en-US', { dateStyle: 'medium' })
const rel = new Intl.RelativeTimeFormat('en-US', { numeric: 'auto' })

export function isZeroTime(iso?: string | null): boolean {
  return !iso || iso.startsWith('0001-01-01')
}

export const fmtInt = (n: number) => num.format(n)
export const fmtFeet = (meters: number) => `${(meters * 3.28084).toFixed(1)} ft`
export const fmtMiles = (miles: number) => `${miles.toFixed(2)} mi`
export const fmtTempF = (c: number) => `${(c * 9 / 5 + 32).toFixed(1)} F`
export const fmtLatLon = (v: number) => v.toFixed(5)
export const fmtPct = (n: number) => `${n.toFixed(1)}%`

export function fmtDate(iso?: string | number | null): string {
  if (iso === undefined || iso === null || iso === '') return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 1900) return ''
  return dateTime.format(d)
}

export function fmtDay(iso?: string | number | null): string {
  if (iso === undefined || iso === null || iso === '') return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 1900) return ''
  return dateOnly.format(d)
}

export function fmtRelative(iso?: string | number | null): string {
  if (iso === undefined || iso === null || iso === '') return ''
  const t = typeof iso === 'number' ? iso : new Date(iso).getTime()
  if (Number.isNaN(t) || new Date(t).getFullYear() < 1900) return ''
  const diff = (t - Date.now()) / 1000
  const abs = Math.abs(diff)
  if (abs < 60) return rel.format(Math.round(diff), 'second')
  if (abs < 3600) return rel.format(Math.round(diff / 60), 'minute')
  if (abs < 86400) return rel.format(Math.round(diff / 3600), 'hour')
  if (abs < 86400 * 30) return rel.format(Math.round(diff / 86400), 'day')
  return fmtDay(t)
}

export function fmtBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return ''
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = n
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v < 10 && i > 0 ? v.toFixed(1) : Math.round(v)} ${units[i]}`
}

export function shortKey(key: string, n = 8): string {
  return key.length > n * 2 ? `${key.slice(0, n)}...${key.slice(-4)}` : key
}

// Alias kept for readability at call sites that mean "date and time".
export const fmtDateTime = fmtDate

// User IDs are "{accountID}:{email}"; show the email part.
export function shortUserId(id: string): string {
  const i = id.indexOf(':')
  return i >= 0 ? id.slice(i + 1) : id
}
