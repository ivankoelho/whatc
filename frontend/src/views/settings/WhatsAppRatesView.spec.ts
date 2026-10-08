// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { h } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { TooltipProvider } from 'reka-ui'
import { createI18n } from 'vue-i18n'
import ptBR from '@/i18n/locales/pt-BR.json'
import en from '@/i18n/locales/en.json'

const rates = vi.hoisted(() => ({ list: vi.fn(), create: vi.fn(), update: vi.fn(), delete: vi.fn() }))
const usage = vi.hoisted(() => ({ reprice: vi.fn() }))
vi.mock('@/services/api', () => ({ whatsappRatesService: rates, whatsappUsageService: usage }))
const toast = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn(), warning: vi.fn() }))
vi.mock('vue-sonner', () => ({ toast }))
const perms = vi.hoisted(() => ({ granted: new Set<string>() }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ hasPermission: (r: string, a: string) => perms.granted.has(`${r}:${a}`) }) }))

import WhatsAppRatesView from './WhatsAppRatesView.vue'

const list = [
  { id: 'r1', country: '55', category: 'utility', price: 0.0068, currency: 'BRL', valid_from: '2026-01-01T00:00:00Z', valid_to: null, in_use: true },
  { id: 'r2', country: '*', category: 'marketing', price: 0.5, currency: 'USD', valid_from: '2026-02-01T00:00:00Z', valid_to: '2026-06-30T00:00:00Z', in_use: false },
]

const $ = (id: string) => document.body.querySelector<HTMLElement>(`[data-testid="${id}"]`)

async function render() {
  const i18n = createI18n({ legacy: false, locale: 'pt-BR', messages: { 'pt-BR': ptBR as any, en: en as any } })
  const w = mount({ render: () => h(TooltipProvider, () => h(WhatsAppRatesView)) }, {
    global: { plugins: [i18n], stubs: { RouterLink: true } },
    attachTo: document.body,
  })
  await flushPromises()
  return w
}

function type(id: string, value: string) {
  const el = $(id) as HTMLInputElement
  el.value = value
  el.dispatchEvent(new Event('input'))
}

const submit = () =>
  Array.from(document.body.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')).find(b => b.textContent!.trim() === 'Create')!.click()

beforeEach(() => {
  document.body.innerHTML = ''
  vi.clearAllMocks()
  perms.granted = new Set(['whatsapp_usage:read', 'whatsapp_usage:write'])
  rates.list.mockResolvedValue({ data: { data: { rates: list } } })
})

describe('WhatsAppRatesView', () => {
  it('lists the prices with the currency of each one and the open period', async () => {
    await render()
    const text = document.body.textContent!
    expect(text).toContain('utility')
    expect(text).toContain('R$')
    expect(text).toContain('0,0068')
    expect(text).toContain('US$')
    expect(text).toContain('Em aberto')
    expect(text).toContain('Qualquer país')
    expect(text).toContain('2026-02-01')
  })

  it('is read-only for whatsapp_usage:read: no add, reprice, edit or delete', async () => {
    perms.granted = new Set(['whatsapp_usage:read'])
    await render()
    expect($('rate-add')).toBeNull()
    expect($('rate-reprice')).toBeNull()
    expect(document.body.querySelectorAll('button[aria-label="Editar preço"], button[aria-label="Excluir preço"]').length).toBe(0)
    expect(document.body.textContent).toContain('utility')
  })

  it('does not offer to delete a price already used by messages', async () => {
    await render()
    expect(document.body.querySelectorAll('button[aria-label="Excluir preço"]').length).toBe(1)
    expect(document.body.textContent).toContain('Em uso')
  })

  it('creates a price with the normalised payload and reloads', async () => {
    rates.create.mockResolvedValue({ data: { data: {} } })
    await render()
    $('rate-add')!.click()
    await flushPromises()
    type('rate-category', ' Utility ')
    type('rate-price', '0.0068')
    type('rate-valid-from', '2026-03-01')
    await flushPromises()
    submit()
    await flushPromises()
    expect(rates.create).toHaveBeenCalledWith({ country: '55', category: 'utility', price: 0.0068, currency: 'BRL', valid_from: '2026-03-01', valid_to: undefined, notes: undefined })
    expect(rates.list).toHaveBeenCalledTimes(2)
  })

  it('refuses to submit without the required fields and does not call the API', async () => {
    await render()
    $('rate-add')!.click()
    await flushPromises()
    submit()
    await flushPromises()
    expect(rates.create).not.toHaveBeenCalled()
    expect(toast.error).toHaveBeenCalled()
  })

  it('locks the price fields of a used price and leaves the period open for closing', async () => {
    await render()
    document.body.querySelector<HTMLButtonElement>('button[aria-label="Editar preço"]')!.click()
    await flushPromises()
    expect(($('rate-price') as HTMLInputElement).disabled).toBe(true)
    expect(($('rate-country') as HTMLInputElement).disabled).toBe(true)
    expect(($('rate-valid-to') as HTMLInputElement).disabled).toBe(false)
  })

  it('keeps the dialog data and reloads nothing when the server refuses (overlap)', async () => {
    rates.create.mockRejectedValue({ response: { status: 409, data: { message: 'Another price for this country and category overlaps this period' } } })
    await render()
    $('rate-add')!.click()
    await flushPromises()
    type('rate-price', '1')
    type('rate-valid-from', '2026-03-01')
    await flushPromises()
    submit()
    await flushPromises()
    expect(toast.error).toHaveBeenCalled()
    expect(rates.list).toHaveBeenCalledTimes(1)
  })

  it('reprices and reports how many messages changed', async () => {
    usage.reprice.mockResolvedValue({ data: { data: { processed: 5, changed: 3 } } })
    await render()
    $('rate-reprice')!.click()
    await flushPromises()
    expect(usage.reprice).toHaveBeenCalledOnce()
    expect(toast.success).toHaveBeenCalledWith('3 de 5 mensagens recalculadas.')
  })

  it('tells the user when repricing is refused because measurement is off', async () => {
    usage.reprice.mockRejectedValue({ response: { status: 409 } })
    await render()
    $('rate-reprice')!.click()
    await flushPromises()
    expect(toast.error).toHaveBeenCalledWith('A medição está desligada neste servidor.')
  })

  it('shows a permission message when the API answers 403', async () => {
    rates.list.mockRejectedValue({ response: { status: 403 } })
    await render()
    expect(document.body.textContent).toContain('Você não tem acesso ao consumo.')
    expect(toast.error).not.toHaveBeenCalled()
  })
})
