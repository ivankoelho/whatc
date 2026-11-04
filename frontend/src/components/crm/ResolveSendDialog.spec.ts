// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'

const store = vi.hoisted(() => ({ resolveProtocolSend: vi.fn() }))
vi.mock('@/stores/occurrences', () => ({ useOccurrencesStore: () => store }))
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), warning: vi.fn() }))
vi.mock('vue-sonner', () => ({ toast }))

import ResolveSendDialog from './ResolveSendDialog.vue'

const i18n = createI18n({ legacy: false, locale: 'en', messages: { en } })
const q = (id: string) => document.querySelector(`[data-testid="${id}"]`) as HTMLInputElement | HTMLTextAreaElement | HTMLButtonElement
const NOTE = 'The customer answered in the conversation, so it arrived.'

async function open() {
  const w = mount(ResolveSendDialog, { props: { open: true, occurrenceId: 'o1', sendId: 's1' }, global: { plugins: [i18n] }, attachTo: document.body })
  await flushPromises()
  return w
}
async function type(el: HTMLInputElement | HTMLTextAreaElement, value: string) {
  el.value = value
  el.dispatchEvent(new Event('input', { bubbles: true }))
  await flushPromises()
}
async function click(el: HTMLElement) {
  el.click()
  await flushPromises()
}

describe('ResolveSendDialog: a person settles a send after checking the conversation', () => {
  beforeEach(() => {
    store.resolveProtocolSend.mockReset()
    Object.values(toast).forEach(f => f.mockReset())
    document.body.innerHTML = ''
  })

  it('nothing can be recorded until the outcome, the check and a justification are all given', async () => {
    await open()
    expect(q('resolve-confirm').disabled).toBe(true)
    await click(q('resolve-outcome-delivered'))
    expect(q('resolve-confirm').disabled).toBe(true) // no check, no reason
    await click(q('resolve-checked'))
    expect(q('resolve-confirm').disabled).toBe(true) // no reason
    await type(q('resolve-note'), 'too short')
    expect(q('resolve-confirm').disabled).toBe(true) // 9 characters
    await type(q('resolve-note'), NOTE)
    expect(q('resolve-confirm').disabled).toBe(false)
    await click(q('resolve-checked')) // unchecking takes it back
    expect(q('resolve-confirm').disabled).toBe(true)
  })

  it('sends exactly the decision: the outcome, the confirmation, the trimmed reason; and tells the page', async () => {
    store.resolveProtocolSend.mockResolvedValue({ state: 'failed', send_id: 's1' })
    const w = await open()
    await click(q('resolve-outcome-not-delivered'))
    await click(q('resolve-checked'))
    await type(q('resolve-note'), `  ${NOTE}  `)
    await click(q('resolve-confirm'))
    expect(store.resolveProtocolSend).toHaveBeenCalledTimes(1)
    expect(store.resolveProtocolSend).toHaveBeenCalledWith('o1', 's1', { outcome: 'not_delivered', conversation_checked: true, note: NOTE })
    expect(toast.success).toHaveBeenCalled()
    expect(w.emitted('resolved')?.[0]).toEqual(['failed'])
    expect(w.emitted('update:open')?.at(-1)).toEqual([false])
  })

  it('choosing "not delivered" warns that a new message will be accepted and could duplicate', async () => {
    await open()
    expect(q('resolve-not-delivered-warning')).toBeNull()
    await click(q('resolve-outcome-not-delivered'))
    expect(q('resolve-not-delivered-warning')).not.toBeNull()
    await click(q('resolve-outcome-delivered'))
    expect(q('resolve-not-delivered-warning')).toBeNull()
  })

  it('a refusal by the server is shown and nothing is closed or announced', async () => {
    store.resolveProtocolSend.mockRejectedValue({ response: { status: 403, data: { status: 'error', message: 'You do not have access to this occurrence' } } })
    const w = await open()
    await click(q('resolve-outcome-delivered'))
    await click(q('resolve-checked'))
    await type(q('resolve-note'), NOTE)
    await click(q('resolve-confirm'))
    expect(toast.error).toHaveBeenCalled()
    expect(w.emitted('resolved')).toBeUndefined()
    expect(w.emitted('update:open')).toBeUndefined()
  })

  it('says the decision is permanent, and cannot be sent twice while it is being recorded', async () => {
    let finish: (v: unknown) => void = () => {}
    store.resolveProtocolSend.mockReturnValue(new Promise(r => { finish = r }))
    await open()
    expect(document.body.textContent).toContain('cannot be undone')
    await click(q('resolve-outcome-delivered'))
    await click(q('resolve-checked'))
    await type(q('resolve-note'), NOTE)
    await click(q('resolve-confirm'))
    expect(q('resolve-confirm').disabled).toBe(true)
    await click(q('resolve-confirm'))
    expect(store.resolveProtocolSend).toHaveBeenCalledTimes(1)
    finish({ state: 'sent', send_id: 's1' })
    await flushPromises()
  })

  it('reopening starts from nothing: no outcome, no check, no old reason', async () => {
    const w = await open()
    await click(q('resolve-outcome-delivered'))
    await click(q('resolve-checked'))
    await type(q('resolve-note'), NOTE)
    await w.setProps({ open: false })
    await w.setProps({ open: true })
    await flushPromises()
    expect(q('resolve-outcome-delivered').checked).toBe(false)
    expect(q('resolve-checked').checked).toBe(false)
    expect(q('resolve-note').value).toBe('')
    expect(q('resolve-confirm').disabled).toBe(true)
  })
})
