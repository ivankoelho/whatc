// @vitest-environment happy-dom
// The card must never offer an action the server would refuse, and must not
// let a person repeat a send whose result is unknown without being warned.
import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'

const list = vi.fn()
const resolve = vi.fn()
vi.mock('@/services/api', () => ({
  occurrenceNotificationsService: { list: (...a: unknown[]) => list(...a), resolve: (...a: unknown[]) => resolve(...a) },
}))
vi.mock('vue-sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import OccurrenceCommunicationCard from './OccurrenceCommunicationCard.vue'

const i18n = createI18n({
  legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false,
  messages: { en: { occurrences: { noticeDuplicateConfirm: 'may duplicate', notice: { uncertain: 'Result unknown', read: 'Read', failed: 'Failed' } } } },
})

function notification(over: Record<string, unknown>) {
  return {
    id: 'n1', status: 'accepted', whatsapp_account: 'Logistica 3040-9602', template_name: 'recebimento_logistica',
    template_language: 'pt_BR', attempts: 1, max_attempts: 3, attempt_history: [], can_retry: false, can_cancel: false, ...over,
  }
}

async function mountWith(items: unknown[]) {
  list.mockResolvedValue({ data: { data: { notifications: items } } })
  const w = mount(OccurrenceCommunicationCard, { props: { occurrenceId: 'o1' }, global: { plugins: [i18n] } })
  await flushPromises()
  return w
}

describe('OccurrenceCommunicationCard', () => {
  beforeEach(() => { list.mockReset(); resolve.mockReset(); resolve.mockResolvedValue({}) })

  it('renders nothing when the case has no communication', async () => {
    const w = await mountWith([])
    expect(w.find('[data-testid="occurrence-communication"]').exists()).toBe(false)
  })

  it('shows state, account and template, and no actions when none is allowed', async () => {
    const w = await mountWith([notification({ status: 'read', provider_message_id: 'wamid.X' })])
    expect(w.get('[data-testid="notification-status"]').text()).toBe('Read')
    expect(w.text()).toContain('Logistica 3040-9602')
    expect(w.text()).toContain('recebimento_logistica')
    expect(w.text()).toContain('wamid.X')
    expect(w.find('[data-testid="notification-retry"]').exists()).toBe(false)
    expect(w.find('[data-testid="notification-cancel"]').exists()).toBe(false)
  })

  it('does not resend an unknown result unless the person confirms the duplicate risk', async () => {
    const w = await mountWith([notification({ status: 'uncertain', can_retry: true, can_cancel: true })])
    const confirm = vi.fn()
    Object.defineProperty(window, 'confirm', { value: confirm, configurable: true, writable: true })

    confirm.mockReturnValueOnce(false)
    await w.get('[data-testid="notification-retry"]').trigger('click')
    await flushPromises()
    expect(resolve).not.toHaveBeenCalled()

    confirm.mockReturnValueOnce(true)
    await w.get('[data-testid="notification-retry"]').trigger('click')
    await flushPromises()
    expect(resolve).toHaveBeenCalledWith('o1', 'n1', 'retry', true)
  })

  it('retries a plain failure without the duplicate acknowledgement', async () => {
    const w = await mountWith([notification({ status: 'failed', can_retry: true })])
    const confirm = vi.fn()
    Object.defineProperty(window, 'confirm', { value: confirm, configurable: true, writable: true })
    await w.get('[data-testid="notification-retry"]').trigger('click')
    await flushPromises()
    expect(confirm).not.toHaveBeenCalled()
    expect(resolve).toHaveBeenCalledWith('o1', 'n1', 'retry', false)
  })
})
