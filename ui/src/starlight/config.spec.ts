import { describe, expect, it } from 'vitest'
// Plain ESM JavaScript; its types live in config.d.mts.
import { BRAND_CSS_ID, DEFAULT_DOCS_BASE, THEME_CSS, brandCSSPlugin, docsSite, rehypeBaseLinks, withBase } from './config.mjs'

describe('withBase', () => {
  it('prefixes root-relative urls and leaves the rest alone', () => {
    expect(withBase('/planning/retirement/', '/docs')).toBe('/docs/planning/retirement/')
    expect(withBase('/planning/retirement/#the-rule', '/docs/')).toBe('/docs/planning/retirement/#the-rule')
    expect(withBase('/docs/planning/', '/docs')).toBe('/docs/planning/')
    expect(withBase('/docsx/', '/docs')).toBe('/docs/docsx/')
    expect(withBase('../other/', '/docs')).toBe('../other/')
    expect(withBase('#anchor', '/docs')).toBe('#anchor')
    expect(withBase('https://example.com/x', '/docs')).toBe('https://example.com/x')
    expect(withBase('//cdn.example.com/x', '/docs')).toBe('//cdn.example.com/x')
    expect(withBase('/x', '/')).toBe('/x')
  })
})

describe('rehypeBaseLinks', () => {
  it('rewrites html and mdx component attributes', () => {
    const tree = {
      type: 'root',
      children: [
        { type: 'element', tagName: 'a', properties: { href: '/taxes/income-tax/' }, children: [] },
        { type: 'element', tagName: 'img', properties: { src: '/images/chart.png', alt: 'x' }, children: [] },
        {
          type: 'mdxJsxFlowElement', name: 'LinkCard',
          attributes: [
            { type: 'mdxJsxAttribute', name: 'href', value: '/investing/income-etfs/' },
            { type: 'mdxJsxAttribute', name: 'title', value: '/not/a/url' },
          ],
          children: [{ type: 'element', tagName: 'a', properties: { href: 'https://x.test/' }, children: [] }],
        },
      ],
    }
    rehypeBaseLinks({ base: '/docs' })(tree)
    const [a, img, card] = tree.children as any[]
    expect(a.properties.href).toBe('/docs/taxes/income-tax/')
    expect(img.properties.src).toBe('/docs/images/chart.png')
    expect(card.attributes[0].value).toBe('/docs/investing/income-etfs/')
    expect(card.attributes[1].value).toBe('/not/a/url')
    expect(card.children[0].properties.href).toBe('https://x.test/')
  })

  it('does nothing at the root base', () => {
    const tree = { type: 'root', children: [{ type: 'element', tagName: 'a', properties: { href: '/x/' }, children: [] }] }
    rehypeBaseLinks({ base: '/' })(tree)
    expect((tree.children[0] as any).properties.href).toBe('/x/')
  })
})

describe('docsSite', () => {
  it('builds the astro config around the supplied integration', () => {
    const calls: Record<string, unknown>[] = []
    const integration = (o: Record<string, unknown>) => { calls.push(o); return { name: 'starlight-stub' } }
    const cfg = docsSite({
      title: 'Acme', base: 'docs/', sidebar: [{ label: 'A', items: [] }], customCss: ['./src/styles/custom.css'],
      integration, vite: { build: { chunkSizeWarningLimit: 700 } }, markdown: { rehypePlugins: ['other'] },
    }) as any
    expect(cfg.base).toBe('/docs')
    expect(cfg.trailingSlash).toBe('ignore')
    expect(cfg.build.format).toBe('directory')
    expect(cfg.vite.build.chunkSizeWarningLimit).toBe(700)
    expect(cfg.integrations[0]).toEqual({ name: 'starlight-stub' })
    expect(cfg.markdown.rehypePlugins[0][0]).toBe(rehypeBaseLinks)
    expect(cfg.markdown.rehypePlugins[0][1]).toEqual({ base: '/docs' })
    expect(cfg.markdown.rehypePlugins[1]).toBe('other')
    expect(calls[0]).toMatchObject({ title: 'Acme', pagefind: true, sidebar: [{ label: 'A', items: [] }] })
    expect(calls[0].customCss).toEqual([THEME_CSS, './src/styles/custom.css'])
  })

  it('defaults the base and requires a title and the integration', () => {
    const cfg = docsSite({ title: 'X', integration: () => ({}) }) as any
    expect(cfg.base).toBe(DEFAULT_DOCS_BASE)
    expect(() => docsSite({ integration: () => ({}) } as any)).toThrow('title is required')
    expect(() => docsSite({ title: 'X' } as any)).toThrow('integration is required')
  })

  it('serves a brand stylesheet between the theme and the product css', () => {
    const calls: Record<string, unknown>[] = []
    const integration = (o: Record<string, unknown>) => { calls.push(o); return {} }
    const cfg = docsSite({
      title: 'Acme', integration, brand: '../acmevue/src/brand.css', customCss: ['./src/styles/custom.css'],
      vite: { plugins: ['other'] },
    }) as any
    expect(calls[0].customCss).toEqual([THEME_CSS, BRAND_CSS_ID, './src/styles/custom.css'])
    expect(cfg.vite.plugins[0].name).toBe('@bboxsys/ui:docs-brand')
    expect(cfg.vite.plugins[1]).toBe('other')
    const plain = docsSite({ title: 'X', integration }) as any
    expect(plain.vite).toEqual({})
    expect(calls[1].customCss).toEqual([THEME_CSS])
  })
})

describe('brandCSSPlugin', () => {
  it('resolves only its own id and maps the file onto the Starlight themes', () => {
    const plugin = brandCSSPlugin('src/brand.css') as any
    expect(plugin.resolveId(BRAND_CSS_ID)).toBe(BRAND_CSS_ID)
    expect(plugin.resolveId('./other.css')).toBeNull()
    expect(plugin.load('./other.css')).toBeNull()
    plugin.configResolved({ root: '/nonexistent/docs' })
    expect(() => plugin.load.call({}, BRAND_CSS_ID)).toThrow(/\/nonexistent\/docs\/src\/brand\.css/)
  })
})
