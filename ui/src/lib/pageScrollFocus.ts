const preservedTargets = [
  '[data-slot="table-container"]',
  '[data-scroll-focus="preserve"]',
  '[role="dialog"]', '[role="alertdialog"]',
  'a', 'area', 'button', 'input', 'select', 'textarea', 'label', 'summary',
  'iframe', 'object', 'embed', 'canvas', 'svg', 'audio', 'video',
  '[contenteditable]:not([contenteditable="false"])',
  '.monaco-editor', '.cm-editor',
  ...[
    'application', 'button', 'checkbox', 'combobox', 'grid', 'img', 'link',
    'listbox', 'menu', 'menuitem', 'menuitemcheckbox', 'menuitemradio',
    'option', 'radio', 'slider', 'spinbutton', 'switch', 'tab', 'textbox', 'tree', 'treeitem',
  ].map(role => `[role="${role}"]`),
].join(', ')

// Background clicks return keyboard scrolling to the surrounding scroll area.
// Controls, table interactions and text selection retain their normal focus.
export function focusPageScroll(event: MouseEvent): void {
  const target = event.target
  const root   = event.currentTarget
  if (!(target instanceof Element) || !(root instanceof HTMLElement) || !root.contains(target)) return
  if (event.defaultPrevented || event.button !== 0 || event.ctrlKey || event.metaKey || event.altKey || event.shiftKey) return
  if (target.closest(preservedTargets)) return
  const focusable = target.closest('[tabindex]:not([tabindex="-1"])')
  if (focusable && !focusable.matches('main, [role="tabpanel"], [role="region"], [role="document"]')) return

  const document = root.ownerDocument
  if (document.querySelector('[aria-modal="true"], dialog[open]')) return
  if (document.getSelection()?.isCollapsed === false) return

  let fallback: HTMLElement | undefined
  let scrollArea: HTMLElement | undefined
  for (let element: Element | null = target; element && root.contains(element); element = element.parentElement) {
    if (!(element instanceof HTMLElement)) continue
    if (!/^(auto|scroll|overlay)$/.test(getComputedStyle(element).overflowY)) continue
    fallback ??= element
    if (element.scrollHeight > element.clientHeight + 1) {
      scrollArea = element
      break
    }
  }
  const focusTarget = scrollArea ?? fallback ?? root
  if (document.activeElement === focusTarget) return

  if (!focusTarget.hasAttribute('tabindex')) {
    focusTarget.setAttribute('tabindex', '-1')
    focusTarget.addEventListener('blur', () => {
      if (focusTarget.getAttribute('tabindex') === '-1') focusTarget.removeAttribute('tabindex')
    }, { once: true })
  }
  focusTarget.focus({ preventScroll: true })
}
