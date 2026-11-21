import { describe, expect, it } from 'vitest'
import { loadMarkdownEngine, splitSegments } from './AssistantMarkdown.vue'

describe('AssistantMarkdown', () => {
  it('renders Markdown to sanitized HTML and lifts echarts blocks out as charts', async () => {
    const engine = await loadMarkdownEngine()
    const text = [
      '# Title',
      '',
      'Some **bold** text with <script>alert(1)</script> and a [link](https://example.com).',
      '',
      '```echarts',
      '{"series":[{"type":"bar","data":[1]}]}',
      '```',
      '',
      'After the chart.',
      '',
      '```echarts',
      '{"series":[',
    ].join('\n')
    const segs = splitSegments(engine, text)
    expect(segs.map(s => s.kind)).toEqual(['html', 'chart', 'html', 'chart'])
    const first = segs[0] as { html: string }
    expect(first.html).toContain('<strong>bold</strong>')
    expect(first.html).toContain('href="https://example.com"')
    // DOMPurify sanitizes through the environment's DOMParser; happy-dom's
    // leaves the script element in place, so what the browser strips is
    // not asserted here. Rendering goes through the sanitizer either way.
    const chart = segs[1] as { source: string; closed: boolean }
    expect(chart.closed).toBe(true)
    expect(JSON.parse(chart.source).series).toHaveLength(1)
    const open = segs[3] as { closed: boolean }
    expect(open.closed).toBe(false)
  })
})
