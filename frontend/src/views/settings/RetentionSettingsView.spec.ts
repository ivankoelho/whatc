// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import ptBR from '@/i18n/locales/pt-BR.json'

const api = vi.hoisted(() => ({
  get: vi.fn(), putPolicy: vi.fn(), simulate: vi.fn(), holds: vi.fn(), createHold: vi.fn(), revokeHold: vi.fn(), history: vi.fn()
}))
vi.mock('@/services/api', () => ({ retentionService: api }))
vi.mock('vue-sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import RetentionSettingsView from './RetentionSettingsView.vue'

const i18n = createI18n({ legacy: false, locale: 'pt-BR', missingWarn: false, fallbackWarn: false, messages: { 'pt-BR': ptBR as never } })
const wrap = (data: unknown) => ({ data: { data } })

const policies = [
  { category: 'occurrence_files', enabled: false, keep_months: 0 },
  { category: 'occurrence_invoices', enabled: true, keep_months: 24, updated_by: 'Ana', updated_at: '2026-10-20T10:00:00Z' },
  { category: 'chat_attachments', enabled: false, keep_months: 0 },
  { category: 'whatsapp_media', enabled: false, keep_months: 0 },
  { category: 'campaign_media', enabled: false, keep_months: 0 }
]
const report = (category: string, applied: boolean) => ({
  category, applied, reason: applied ? '' : 'no_policy', months: 24, assumed: false, bytes_partial: false,
  buckets: { eligible: { count: 3, bytes: 3 << 20 }, not_due: { count: 1, bytes: 0 }, open: { count: 0, bytes: 0 }, held: { count: 2, bytes: 0 }, shared: { count: 0, bytes: 0 }, quarantine: { count: 0, bytes: 0 } },
  samples: applied ? [{ id: 'aaaaaaaa-1', name: 'contrato.pdf', bytes: 10, bucket: 'eligible', reason: 'protocol closed more than 24 months ago', related: '2026-000010' }] : [],
  protected_samples: []
})

async function mountView() {
  const w = mount(RetentionSettingsView, { global: { plugins: [i18n] } })
  await flushPromises()
  return w
}

describe('RetentionSettingsView', () => {
  beforeEach(() => {
    Object.values(api).forEach(f => f.mockReset())
    api.get.mockResolvedValue(wrap({ policies, not_covered: ['meta_hosted_media', 'audit_logs'], engine: { execution_enabled: false } }))
    api.holds.mockResolvedValue(wrap({ holds: [] }))
    api.history.mockResolvedValue(wrap({ history: [] }))
  })

  it('says plainly that nothing is deleted, shows each category off by default and lists what is out of scope', async () => {
    const w = await mountView()
    expect(w.get('[data-testid="retention-engine-off"]').text()).toContain('nenhum arquivo é apagado')
    expect(w.findAll('[data-testid^="retention-row-"]')).toHaveLength(5)
    expect(w.text()).toContain('Mídias hospedadas pela Meta')
    expect(w.text()).toContain('Registros de auditoria')
    // nothing to save until something changes; no way to delete anything from this screen
    expect(w.get('[data-testid="retention-save-occurrence_files"]').attributes('disabled')).toBeDefined()
    expect(w.text()).not.toMatch(/Excluir agora|Apagar agora/)
  })

  it('refuses to enable a category without a term, and saves a valid one', async () => {
    const w = await mountView()
    const input = w.get('#ret-m-occurrence_files')
    await input.setValue('36')
    await w.get('[data-testid="retention-save-occurrence_files"]').trigger('click')
    await flushPromises()
    expect(api.putPolicy).toHaveBeenCalledWith('occurrence_files', { keep_months: 36, enabled: false })
  })

  it('simulates read-only with the typed terms and shows eligible, protected and the reason a category was skipped', async () => {
    api.simulate.mockResolvedValue(wrap({ generated_at: '2026-10-26T12:00:00Z', categories: [report('occurrence_invoices', true), report('occurrence_files', false)] }))
    const w = await mountView()
    await w.get('#ret-m-occurrence_files').setValue('12')
    await w.get('[data-testid="retention-simulate"]').trigger('click')
    await flushPromises()
    expect(api.simulate).toHaveBeenCalledWith({ months: { occurrence_files: 12, occurrence_invoices: 24 } })
    expect(api.putPolicy).not.toHaveBeenCalled()
    const row = w.get('[data-testid="retention-sim-occurrence_invoices"]')
    expect(row.text()).toContain('3') // eligible
    expect(row.text()).toContain('3.0 MB')
    expect(w.get('[data-testid="retention-sim-occurrence_files"]').text()).toContain('Sem regra configurada')
    await row.get('button').trigger('click') // open the samples
    expect(w.text()).toContain('contrato.pdf')
    expect(w.text()).toContain('2026-000010')
  })

  it('a hold needs a protocol and a real justification; revoking asks for one too', async () => {
    api.holds.mockResolvedValue(wrap({ holds: [{ id: 'h1', scope_type: 'occurrence', scope_label: '2026-000010', reason: 'Litígio', created_by: 'Ana', created_at: '2026-10-20T10:00:00Z', active: true }] }))
    const w = await mountView()
    expect(w.get('[data-testid="retention-holds"]').text()).toContain('2026-000010')
    await w.get('[data-testid="retention-new-hold"]').trigger('click')
    await flushPromises()
    const confirm = () => document.body.querySelector('[data-testid="retention-confirm-hold"]') as HTMLButtonElement
    expect(confirm().disabled).toBe(true)
    ;(document.body.querySelector('#hold-protocol') as HTMLInputElement).value = '2026-000011'
    ;(document.body.querySelector('#hold-protocol') as HTMLInputElement).dispatchEvent(new Event('input'))
    ;(document.body.querySelector('#hold-reason') as HTMLTextAreaElement).value = 'curto'
    ;(document.body.querySelector('#hold-reason') as HTMLTextAreaElement).dispatchEvent(new Event('input'))
    await flushPromises()
    expect(confirm().disabled).toBe(true)
    ;(document.body.querySelector('#hold-reason') as HTMLTextAreaElement).value = 'Cliente abriu processo judicial'
    ;(document.body.querySelector('#hold-reason') as HTMLTextAreaElement).dispatchEvent(new Event('input'))
    await flushPromises()
    expect(confirm().disabled).toBe(false)
    confirm().click()
    await flushPromises()
    expect(api.createHold).toHaveBeenCalledWith({ scope_type: 'occurrence', protocol_number: '2026-000011', reason: 'Cliente abriu processo judicial' })
  })
})
