// @vitest-environment happy-dom
// The customer-facing card must never look or behave like the internal one, and
// must not show a conversation to someone who only follows the case.
import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'

const get = vi.fn()
const sendText = vi.fn()
const sendTemplate = vi.fn()
vi.mock('@/services/api', () => ({
  caseWhatsAppService: {
    get: (...a: unknown[]) => get(...a),
    sendText: (...a: unknown[]) => sendText(...a),
    sendTemplate: (...a: unknown[]) => sendTemplate(...a),
  },
}))
vi.mock('vue-sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import OccurrenceWhatsAppCard from './OccurrenceWhatsAppCard.vue'

const i18n = createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false, messages: { en: {} } })

async function mountWith(data: Record<string, unknown>) {
  get.mockResolvedValue({ data: { data: data } })
  const w = mount(OccurrenceWhatsAppCard, { props: { occurrenceId: 'o1' }, global: { plugins: [i18n] } })
  await flushPromises()
  return w
}

describe('OccurrenceWhatsAppCard', () => {
  beforeEach(() => { get.mockReset(); sendText.mockReset(); sendTemplate.mockReset() })

  it('shows nothing of the conversation to someone who only follows the case', async () => {
    const w = await mountWith({ configured: true, restricted: true, account: 'Logistica' })
    expect(w.find('[data-testid="wa-restricted"]').exists()).toBe(true)
    expect(w.find('[data-testid="wa-messages"]').exists()).toBe(false)
    expect(w.find('[data-testid="wa-text"]').exists()).toBe(false)
    expect(w.emitted('configured')?.[0]).toEqual([true])
  })

  it('offers free text only inside the window, otherwise just templates', async () => {
    const msgs = [{ id: 'm1', direction: 'outgoing', type: 'template', content: 'Olá', status: 'delivered', created_at: '2026-10-25T10:00:00Z' }]
    const tpls = [{ name: 'aviso', language: 'pt_BR', body: 'Aviso {{1}}', params: ['1'] }]
    const closed = await mountWith({ configured: true, restricted: false, account: 'L', can_reply: true, window_open: false, messages: msgs, templates: tpls })
    expect(closed.find('[data-testid="wa-text"]').exists()).toBe(false)
    expect(closed.find('[data-testid="wa-template"]').exists()).toBe(true)
    expect(closed.find('[data-testid="wa-messages"]').exists()).toBe(true)

    const open = await mountWith({ configured: true, restricted: false, account: 'L', can_reply: true, window_open: true, messages: msgs, templates: tpls })
    expect(open.find('[data-testid="wa-text"]').exists()).toBe(true)
  })

  it('sends text to the customer through its own endpoint', async () => {
    sendText.mockResolvedValue({})
    const w = await mountWith({ configured: true, restricted: false, account: 'L', can_reply: true, window_open: true, messages: [], templates: [] })
    await w.get('[data-testid="wa-text"]').setValue('Ligamos já')
    await w.get('[data-testid="wa-send-text"]').trigger('click')
    await flushPromises()
    expect(sendText).toHaveBeenCalledWith('o1', 'Ligamos já')
  })

  it('does not send a template with an empty parameter', async () => {
    const tpls = [{ name: 'aviso', language: 'pt_BR', body: 'Aviso {{1}}', params: ['1'] }]
    const w = await mountWith({ configured: true, restricted: false, account: 'L', can_reply: true, window_open: false, messages: [], templates: tpls })
    await w.get('[data-testid="wa-template"]').setValue('aviso')
    await flushPromises()
    expect(w.get('[data-testid="wa-send-template"]').attributes('disabled')).toBeDefined()
    expect(sendTemplate).not.toHaveBeenCalled()
  })
})
