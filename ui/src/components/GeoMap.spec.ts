import { describe, it, expect } from 'vitest'
import { tooltipContent } from './GeoMap.vue'

// GeoMap builds Leaflet tooltips from a DOM node (textContent), never an HTML
// string, so user-controlled labels (waypoint/route/lake names) cannot inject
// markup. Mounting Leaflet in happy-dom is impractical, so we test the escaping
// invariant of the helper directly.
describe('GeoMap tooltipContent (XSS regression)', () => {
  it('stores the raw text as textContent, not as parsed markup', () => {
    const payload = '<img src=x onerror=alert(1)>'
    const span = tooltipContent(payload)
    // The dangerous text is preserved verbatim as text...
    expect(span.textContent).toBe(payload)
    // ...and no live element was created from it.
    expect(span.querySelector('img')).toBeNull()
    expect(span.children.length).toBe(0)
  })

  it('serializes to an escaped HTML string (no live tags)', () => {
    const span = tooltipContent('<script>alert(1)</script>')
    // innerHTML reflects the ESCAPED form, so nothing executes when rendered.
    expect(span.innerHTML).not.toContain('<script>')
    expect(span.innerHTML).toContain('&lt;script&gt;')
  })

  it('leaves ordinary labels intact', () => {
    const span = tooltipContent('Lake Travis')
    expect(span.textContent).toBe('Lake Travis')
    expect(span.innerHTML).toBe('Lake Travis')
  })
})
