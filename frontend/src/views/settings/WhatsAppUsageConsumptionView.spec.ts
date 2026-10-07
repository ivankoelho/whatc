// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { h } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { TooltipProvider } from 'reka-ui'
import { createI18n } from 'vue-i18n'
import ptBR from '@/i18n/locales/pt-BR.json'
import en from '@/i18n/locales/en.json'

const usage = vi.hoisted(() => ({ summary: vi.fn(), messages: vi.fn(), setRecording: vi.fn() }))
const units = vi.hoisted(() => ({ list: vi.fn() }))
const accounts = vi.hoisted(() => ({ list: vi.fn() }))
vi.mock('@/services/api', () => ({ whatsappUsageService: usage, unitsService: units, accountsService: accounts }))
const toast = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn(), warning: vi.fn() }))
vi.mock('vue-sonner', () => ({ toast }))
const perms = vi.hoisted(() => ({ granted: new Set<string>() }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ hasPermission: (r: string, a: string) => perms.granted.has(`${r}:${a}`) }) }))

import WhatsAppUsageConsumptionView from './WhatsAppUsageConsumptionView.vue'

const counts = (over: Record<string, number> = {}) => ({
  total: 12, billed: 3, not_billable: 1, awaiting_pricing: 2, no_rate: 1, unconfirmed: 1, failed: 1, send_failed: 1,
  pending: 2, unclassified: 4, unlinked: 2, unlinked_attention: 0, ...over,
})

function summary(over: Record<string, unknown> = {}) {
  return {
    period: { from: '2026-09-08', to: '2026-10-07', timezone: 'America/Bahia' },
    recording: { enabled: true, server_enabled: true, panel_enabled: true, changed_at: null, since: '2026-10-01T10:00:00Z' },
    counts: counts(),
    costs: [{ currency: 'BRL', estimated_cost: 0.3, messages: 2 }, { currency: 'USD', estimated_cost: 0.05, messages: 1 }],
    provisional_cost: [],
    unlinked_attention_hours: 24,
    groups: [{ key: '2026-10-06', messages: 5, billed: 3, costs: [{ currency: 'BRL', estimated_cost: 0.3, messages: 2 }] }],
    ...over,
  }
}

const $ = (id: string) => document.body.querySelector<HTMLElement>(`[data-testid="${id}"]`)

async function render() {
  const i18n = createI18n({ legacy: false, locale: 'pt-BR', messages: { 'pt-BR': ptBR as any, en: en as any } })
  const w = mount({ render: () => h(TooltipProvider, () => h(WhatsAppUsageConsumptionView)) }, {
    global: { plugins: [i18n], stubs: { RouterLink: true } },
    attachTo: document.body,
  })
  await flushPromises()
  return w
}

beforeEach(() => {
  document.body.innerHTML = ''
  vi.clearAllMocks()
  perms.granted = new Set(['whatsapp_usage:read', 'whatsapp_usage:write'])
  usage.summary.mockResolvedValue({ data: { data: summary() } })
  usage.messages.mockResolvedValue({ data: { data: { messages: [], total: 0, page: 1, limit: 20 } } })
  units.list.mockResolvedValue({ data: { data: { units: [{ id: 'u1', name: 'Loja Centro', active: true }, { id: 'u2', name: 'Loja Antiga', active: false }] } } })
  accounts.list.mockResolvedValue({ data: { data: { accounts: [{ name: 'principal' }] } } })
})

describe('WhatsAppUsageConsumptionView', () => {
  it('shows the cost of each currency on its own, never a combined total', async () => {
    await render()
    expect($('usage-cost-BRL')!.textContent).toContain('R$')
    expect($('usage-cost-BRL')!.textContent).toContain('0,30')
    expect($('usage-cost-USD')!.textContent).toContain('US$')
    expect($('usage-cost-USD')!.textContent).toContain('0,05')
    expect($('usage-card-cost')!.textContent).toContain('Moedas nunca são somadas')
  })

  it('shows the counters of the states that need attention', async () => {
    await render()
    expect($('usage-card-total')!.textContent).toContain('12')
    expect($('usage-card-awaiting')!.textContent).toContain('Sem informação da Meta')
    expect($('usage-card-awaiting')!.textContent).toContain('2')
    expect($('usage-card-no-rate')!.textContent).toContain('1')
    expect($('usage-card-unclassified')!.textContent).toContain('4')
    expect($('usage-card-unlinked')!.textContent).toContain('Aguardando a mensagem')
    expect($('usage-card-failed')!.textContent).toContain('2') // failed + send_failed
  })

  it('highlights an unlinked backlog only when some are older than the attention hours', async () => {
    await render()
    expect($('usage-unlinked-attention')).toBeNull()

    document.body.innerHTML = ''
    usage.summary.mockResolvedValue({ data: { data: summary({ counts: counts({ unlinked_attention: 1 }) }) } })
    await render()
    expect($('usage-unlinked-attention')!.textContent).toContain('há mais de 24 h')
  })

  it('says measurement is OFF instead of showing a misleading empty table', async () => {
    usage.summary.mockResolvedValue({ data: { data: summary({ recording: { enabled: false, server_enabled: false, panel_enabled: true, changed_at: null, since: null }, counts: counts({ total: 0 }), costs: [] }) } })
    await render()
    expect($('usage-recording-off')!.textContent).toContain('A medição está desligada')
    expect($('usage-recording-since')).toBeNull()
  })

  it('says nothing was recorded yet, and since when it is measuring', async () => {
    usage.summary.mockResolvedValue({ data: { data: summary({ recording: { enabled: true, server_enabled: true, panel_enabled: true, changed_at: null, since: null } }) } })
    await render()
    expect($('usage-recording-none')).not.toBeNull()

    document.body.innerHTML = ''
    usage.summary.mockResolvedValue({ data: { data: summary() } })
    await render()
    expect($('usage-recording-since')!.textContent).toContain('Medindo desde')
    expect($('usage-recording-since')!.textContent).toContain('não podem ser reconstruídas')
  })

  it('shows the provisional cost apart, only when there is one', async () => {
    await render()
    expect($('usage-card-provisional')).toBeNull()

    document.body.innerHTML = ''
    usage.summary.mockResolvedValue({ data: { data: summary({ provisional_cost: [{ currency: 'BRL', estimated_cost: 0.14, messages: 2 }] }) } })
    await render()
    expect($('usage-provisional-BRL')!.textContent).toContain('0,14')
    expect($('usage-card-provisional')!.textContent).toContain('nunca gravado')
  })

  it('asks for the last 30 days grouped by day by default, and the list page by page', async () => {
    await render()
    const q = usage.summary.mock.calls[0][0]
    expect(q.group_by).toBe('day')
    expect(q.from).toMatch(/^\d{4}-\d{2}-\d{2}$/)
    expect(q.to).toMatch(/^\d{4}-\d{2}-\d{2}$/)
    expect(q.unit_id).toBeUndefined() // the "all" sentinel never reaches the API
    expect(usage.messages.mock.calls[0][0]).toMatchObject({ page: 1, limit: 20 })
  })

  it('loads the unit filter from the units service (inactive ones follow selectableOptions)', async () => {
    await render()
    expect($('usage-filter-unit')).not.toBeNull()
    expect(units.list).toHaveBeenCalled()
  })

  it('lists the ledger rows without any message content', async () => {
    usage.messages.mockResolvedValue({ data: { data: { total: 1, page: 1, limit: 20, messages: [{
      id: 'm1', whatsapp_account: 'principal', direction: 'outgoing', actor_type: 'agent', origin_inferred: false, billable: true,
      billing_state: 'priced', link_state: 'unlinked', estimated_cost: 0.0068, estimated_currency: 'BRL', billing_category: 'utility',
      sent_at: '2026-10-06T12:00:00Z', unit_name: 'Loja Centro', agent_name: 'Milena Souza',
    }] } } })
    await render()
    const text = document.body.textContent!
    expect(text).toContain('Milena Souza')
    expect(text).toContain('Loja Centro')
    expect(text).toContain('Com preço')
    expect(text).toContain('Sem vínculo')
    expect(text).toContain('0,0068')
  })

  it('shows a permission message on 403 and no error toast', async () => {
    usage.summary.mockRejectedValue({ response: { status: 403 } })
    await render()
    expect(document.body.textContent).toContain('Você não tem acesso ao consumo.')
    expect(toast.error).not.toHaveBeenCalled()
  })
})

describe('WhatsAppUsageConsumptionView: the measurement switch', () => {
  const rec = (over: Record<string, unknown>) => ({ enabled: true, server_enabled: true, panel_enabled: true, changed_at: null, since: '2026-10-01T10:00:00Z', ...over })

  it('shows the switch, on, to someone who can write', async () => {
    await render()
    const sw = $('usage-recording-switch')!
    expect(sw).not.toBeNull()
    expect(sw.getAttribute('aria-checked')).toBe('true')
    expect($('usage-recording-card')!.textContent).toContain('Medir o consumo')
  })

  it('hides the switch from a reader: they only see the state', async () => {
    perms.granted = new Set(['whatsapp_usage:read'])
    await render()
    expect($('usage-recording-switch')).toBeNull()
    expect($('usage-recording-since')).not.toBeNull()
  })

  it('hides the switch when the server turned the measurement off, and says so', async () => {
    usage.summary.mockResolvedValue({ data: { data: summary({ recording: rec({ enabled: false, server_enabled: false, since: null }) }) } })
    await render()
    expect($('usage-recording-switch')).toBeNull()
    expect($('usage-recording-off-server')!.textContent).toContain('usage.record_enabled')
  })

  it('turns it off, tells the user and reloads the figures', async () => {
    usage.setRecording.mockResolvedValue({ data: { data: rec({ enabled: false, panel_enabled: false }) } })
    await render()
    const before = usage.summary.mock.calls.length
    $('usage-recording-switch')!.click()
    await flushPromises()
    expect(usage.setRecording).toHaveBeenCalledWith(false)
    expect(toast.success).toHaveBeenCalledWith('Medição desligada.')
    expect(usage.summary.mock.calls.length).toBeGreaterThan(before)
  })

  it('turns it back on from the off state', async () => {
    usage.summary.mockResolvedValue({ data: { data: summary({ recording: rec({ enabled: false, panel_enabled: false, changed_at: '2026-10-07T10:00:00Z' }) }) } })
    usage.setRecording.mockResolvedValue({ data: { data: rec({}) } })
    await render()
    expect($('usage-recording-switch')!.getAttribute('aria-checked')).toBe('false')
    $('usage-recording-switch')!.click()
    await flushPromises()
    expect(usage.setRecording).toHaveBeenCalledWith(true)
    expect(toast.success).toHaveBeenCalledWith('Medição ligada.')
  })

  it('explains that it was turned off on the screen, since when, and that the period is not reconstructed', async () => {
    usage.summary.mockResolvedValue({ data: { data: summary({ recording: rec({ enabled: false, panel_enabled: false, changed_at: '2026-10-07T10:00:00Z' }) }) } })
    await render()
    const alert = $('usage-recording-off')!
    expect(alert.textContent).toContain('A medição está desligada')
    expect($('usage-recording-off-panel')!.textContent).toContain('desligada nesta tela')
    expect($('usage-recording-off-panel')!.textContent).toContain('não incluem esse período')
    expect($('usage-recording-off-server')).toBeNull()
  })

  it('mentions when it was last switched on again', async () => {
    usage.summary.mockResolvedValue({ data: { data: summary({ recording: rec({ changed_at: '2026-10-07T10:00:00Z' }) }) } })
    await render()
    expect($('usage-recording-resumed')!.textContent).toContain('Última alteração')
  })

  it('shows an error and leaves the state alone when the server refuses', async () => {
    usage.setRecording.mockRejectedValue({ response: { status: 409, data: { message: 'turned off on this server' } } })
    await render()
    $('usage-recording-switch')!.click()
    await flushPromises()
    expect(toast.error).toHaveBeenCalled()
    expect(toast.success).not.toHaveBeenCalled()
  })
})
