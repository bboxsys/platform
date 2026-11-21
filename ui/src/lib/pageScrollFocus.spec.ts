import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { focusPageScroll } from './pageScrollFocus'

let root: HTMLElement
let page: HTMLElement
let tableControl: HTMLButtonElement

function scrollable(element: HTMLElement, height = 400, content = 1000) {
  element.style.overflowY = 'auto'
  Object.defineProperties(element, {
    clientHeight: { configurable: true, value: height },
    scrollHeight: { configurable: true, value: content },
  })
}
function background() { return page.querySelector<HTMLElement>('[data-background]')! }

beforeEach(() => {
  root = document.createElement('main')
  root.innerHTML = '<section data-page><p data-background>Page content</p><div data-slot="table-container"><button>Table control</button><span data-cell>Cell</span></div></section>'
  document.body.append(root)
  root.addEventListener('click', focusPageScroll)
  page = root.querySelector('[data-page]')!
  tableControl = root.querySelector('button')!
  scrollable(root)
  scrollable(page)
  tableControl.focus()
})
afterEach(() => {
  document.getSelection()?.removeAllRanges()
  document.body.replaceChildren()
  vi.restoreAllMocks()
})

describe('focusPageScroll', () => {
  it('focuses the nearest page scroll area after a background click without moving the viewport', () => {
    const focus = vi.spyOn(page, 'focus')
    background().click()
    expect(document.activeElement).toBe(page)
    expect(focus).toHaveBeenCalledWith({ preventScroll: true })
    expect(page.tabIndex).toBe(-1)
    tableControl.focus()
    expect(page.hasAttribute('tabindex')).toBe(false)
  })

  it('chooses an overflowing page over an unbounded inner wrapper', () => {
    const wrapper = document.createElement('div')
    scrollable(wrapper, 100, 100)
    page.append(wrapper)
    wrapper.append(background())
    background().click()
    expect(document.activeElement).toBe(page)
  })

  it('falls back to the shared main container for views without their own scroller', () => {
    page.style.overflowY = 'visible'
    background().click()
    expect(document.activeElement).toBe(root)
  })

  it('allows background clicks inside focusable tab panels', () => {
    const panel = document.createElement('div')
    panel.setAttribute('role', 'tabpanel')
    panel.tabIndex = 0
    root.append(panel)
    panel.append(page)
    background().click()
    expect(document.activeElement).toBe(page)
  })

  it('retains existing focus attributes on a page scroll region', () => {
    page.tabIndex = -1
    background().click()
    tableControl.focus()
    expect(page.getAttribute('tabindex')).toBe('-1')
  })

  it('keeps table clicks inside the table', () => {
    page.querySelector<HTMLElement>('[data-cell]')!.click()
    expect(document.activeElement).toBe(tableControl)
  })

  it.each([
    '<button>Action</button>', '<input>', '<textarea></textarea>', '<select><option>One</option></select>',
    '<a href="#details">Details</a>', '<label>Label</label>', '<summary>Notes</summary>',
    '<div role="checkbox">Toggle</div>', '<div role="tab">Tab</div>', '<div tabindex="0">Custom control</div>',
    '<div contenteditable="true">Editor</div>', '<div class="monaco-editor">Editor</div>',
    '<div role="img">Chart</div>', '<canvas></canvas>', '<div data-scroll-focus="preserve">Custom widget</div>',
    '<div role="dialog">Dialog content</div>',
  ])('preserves interactions with %s', markup => {
    const wrapper = document.createElement('div')
    wrapper.innerHTML = markup
    page.append(wrapper)
    const control = wrapper.firstElementChild as HTMLElement
    control.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(document.activeElement).toBe(tableControl)
  })

  it('leaves focus in an open modal', () => {
    const modal = document.createElement('div')
    modal.setAttribute('aria-modal', 'true')
    modal.innerHTML = '<input>'
    document.body.append(modal)
    const input = modal.querySelector('input')!
    input.focus()
    background().click()
    expect(document.activeElement).toBe(input)
  })

  it('preserves text selection', () => {
    const range = document.createRange()
    range.selectNodeContents(background())
    document.getSelection()!.removeAllRanges()
    document.getSelection()!.addRange(range)
    expect(document.getSelection()!.isCollapsed).toBe(false)
    background().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(document.activeElement).toBe(tableControl)
  })

  it('respects clicks handled by a view and modifier clicks', () => {
    background().addEventListener('click', event => event.preventDefault(), { once: true })
    background().dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    expect(document.activeElement).toBe(tableControl)
    background().dispatchEvent(new MouseEvent('click', { bubbles: true, ctrlKey: true }))
    expect(document.activeElement).toBe(tableControl)
  })
})
