import { describe, it, expect, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { nextTick } from 'vue'
import ConfirmDialog from './ConfirmDialog.vue'

// reka-ui teleports the dialog content to document.body; query it there.
function cancelButton(): HTMLButtonElement | null {
  return document.body.querySelector('[data-slot="alert-dialog-cancel"]')
}
function actionButton(): HTMLButtonElement | null {
  return document.body.querySelector('[data-slot="alert-dialog-action"]')
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('ConfirmDialog', () => {
  it('renders Cancel and Confirm when open', async () => {
    mount(ConfirmDialog, {
      props: { title: 'Delete it?', action: async () => {}, open: true },
      attachTo: document.body,
    })
    await flushPromises() // reka teleports the content on the next tick
    expect(cancelButton()).not.toBeNull()
    expect(actionButton()).not.toBeNull()
  })

  it('keeps Cancel enabled while the action is pending (wedge regression)', async () => {
    // Drive the internal "busy" flag directly: reka's AlertDialogAction closes
    // the dialog on a real click, so we cannot hold a pending click while the
    // content stays mounted. Setting busy reproduces the exact in-flight render
    // and lets us assert the template's disabled contract.
    const wrapper = mount(ConfirmDialog, {
      props: { title: 'Delete it?', action: () => new Promise<void>(() => {}), open: true },
      attachTo: document.body,
    })
    await flushPromises() // let the teleported content mount
    ;(wrapper.vm as unknown as { busy: boolean }).busy = true
    await nextTick()

    // The confirm button is disabled to prevent a double submit...
    expect(actionButton()!.disabled).toBe(true)
    expect(actionButton()!.textContent).toContain('Working')
    // ...but Cancel must remain enabled so a hung action cannot wedge the
    // dialog open forever.
    expect(cancelButton()!.disabled).toBe(false)
    expect(cancelButton()!.hasAttribute('disabled')).toBe(false)
  })
})
