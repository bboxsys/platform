import { describe, it, expect } from 'vitest'
import { defineComponent, h } from 'vue'
import { mount } from '@vue/test-utils'
import AccentSurface from './AccentSurface.vue'
import AppNotice from './AppNotice.vue'
import AppNoticeBand from './AppNoticeBand.vue'
import MetricTile from './MetricTile.vue'
import TermList from './TermList.vue'
import { ACCENT_SURFACE, RAIL, RAIL_METER, TONE_NOTICE } from '../theme/tone'

describe('AccentSurface', () => {
  it('wears the brand accent by default and renders as a section', () => {
    const wrapper = mount(AccentSurface, { slots: { default: 'body' } })
    expect(wrapper.element.tagName).toBe('SECTION')
    expect(wrapper.classes()).toContain('border-brand/30')
    expect(wrapper.find('div').classes()).toContain(ACCENT_SURFACE.brand.rail)
    expect(wrapper.text()).toBe('body')
  })

  it('takes another accent and another element', () => {
    const wrapper = mount(AccentSurface, { props: { tone: 'chart-5', as: 'article' } })
    expect(wrapper.element.tagName).toBe('ARTICLE')
    expect(wrapper.find('div').classes()).toContain('bg-chart-5')
  })

  it('holds no literal color', () => {
    for (const { frame, rail } of Object.values(ACCENT_SURFACE)) {
      expect(`${frame} ${rail}`).not.toMatch(/#|rgb|(red|blue|cyan|orange|emerald|amber)-\d/)
    }
  })
})

describe('AppNotice', () => {
  it('renders in place when inline', () => {
    const wrapper = mount(AppNotice, { props: { inline: true, tone: 'bad' }, slots: { default: 'Cluster unreachable' } })
    const notice = wrapper.find('[data-slot="app-notice"]')
    expect(notice.text()).toBe('Cluster unreachable')
    for (const cls of TONE_NOTICE.bad.split(' ')) expect(notice.classes()).toContain(cls)
  })

  it('teleports into the band', async () => {
    const Page = defineComponent({
      render: () => h('div', [h(AppNoticeBand), h('main', [h(AppNotice, null, () => 'Inventory is stale')])]),
    })
    const wrapper = mount(Page, { attachTo: document.body })
    await new Promise(resolve => setTimeout(resolve))
    const band = document.getElementById('app-notices')!
    expect(band.textContent).toContain('Inventory is stale')
    expect(wrapper.find('main').text()).toBe('')
    wrapper.unmount()
  })
})

describe('MetricTile', () => {
  it('shows label, value and caption on the brand rail', () => {
    const wrapper = mount(MetricTile, { props: { label: 'Routes', value: '12', caption: '3 lakes' } })
    expect(wrapper.text()).toContain('Routes')
    expect(wrapper.text()).toContain('12')
    expect(wrapper.text()).toContain('3 lakes')
    expect(wrapper.element.tagName).toBe('DIV')
    for (const cls of RAIL.brand.split(' ')) expect(wrapper.classes()).toContain(cls)
  })

  it('draws a meter in the rail color in place of the caption', () => {
    const wrapper = mount(MetricTile, { props: { label: 'Disk', value: '91%', meter: 91, rail: 'bad', caption: 'hidden' } })
    expect(wrapper.text()).not.toContain('hidden')
    expect(wrapper.find('[data-slot="progress-indicator"]').classes()).toContain(RAIL_METER.bad)
  })

  it('acts as a button when clickable', async () => {
    const wrapper = mount(MetricTile, { props: { label: 'Alerts', value: '2', clickable: true } })
    expect(wrapper.attributes('role')).toBe('button')
    await wrapper.trigger('click')
    await wrapper.trigger('keydown', { key: 'Enter' })
    expect(wrapper.emitted('click')).toHaveLength(2)
  })

  it('is a plain anchor when the app has no router', () => {
    const wrapper = mount(MetricTile, { props: { label: 'Routes', value: '12', to: '/routes' } })
    expect(wrapper.element.tagName).toBe('A')
    expect(wrapper.attributes('href')).toBe('/routes')
  })

  it('uses the RouterLink the app registered', () => {
    const RouterLink = defineComponent({
      props: { to: { type: String, required: true } },
      setup: (props, { slots }) => () => h('a', { 'data-router-link': props.to }, slots.default?.()),
    })
    const wrapper = mount(MetricTile, {
      props: { label: 'Routes', value: '12', to: '/routes' },
      global: { components: { RouterLink } },
    })
    expect(wrapper.attributes('data-router-link')).toBe('/routes')
  })
})

describe('TermList', () => {
  it('caps the list and counts the rest', () => {
    const wrapper = mount(TermList, { props: { terms: ['a', 'b', 'c', 'd', 'e', 'f'], limit: 4 } })
    const overflow = wrapper.find('[data-slot="term-overflow"]')
    expect(overflow.text()).toBe('+2')
    expect(overflow.attributes('title')).toBe('e, f')
    expect(wrapper.text()).not.toContain('e+')
  })

  it('shows the empty mark for no terms', () => {
    const wrapper = mount(TermList, { props: { terms: [], empty: 'any' } })
    expect(wrapper.text()).toBe('any')
  })
})
