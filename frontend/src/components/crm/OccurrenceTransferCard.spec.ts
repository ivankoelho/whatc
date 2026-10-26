// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'

const svc = vi.hoisted(() => ({ listTransfers: vi.fn(), forward: vi.fn(), returnCase: vi.fn(), forwardTargets: vi.fn(), teamMembers: vi.fn() }))
vi.mock('@/services/api', () => ({ occurrencesService: svc }))
vi.mock('vue-sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import OccurrenceTransferCard from './OccurrenceTransferCard.vue'

const i18n = createI18n({ legacy: false, locale: 'en', messages: { en } })
const ok = (data: unknown) => Promise.resolve({ data: { data } })
const mountCard = () => mount(OccurrenceTransferCard, { props: { occurrenceId: 'o1', teamName: 'Logística', assignedName: 'Ana' }, global: { plugins: [i18n] }, attachTo: document.body })

describe('OccurrenceTransferCard', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    Object.values(svc).forEach(f => f.mockReset())
    svc.listTransfers.mockReturnValue(ok({ transfers: [{ id: 't1', kind: 'forward', from_team: 'SAC', to_team: 'Logística', by: 'Bia', reason: 'Dados completos', created_at: '2026-10-26T13:05:00Z' }], can_forward: true, can_return: true, return_to: 'SAC' }))
    svc.forwardTargets.mockReturnValue(ok({ targets: [{ id: 'tv', name: 'Vendas' }, { id: 'tl', name: 'Logística' }] }))
    svc.teamMembers.mockReturnValue(ok({ members: [{ id: 'u9', full_name: 'Caio' }] }))
    svc.forward.mockReturnValue(ok({}))
    svc.returnCase.mockReturnValue(ok({}))
  })

  it('shows department, owner and the movements; offers both actions only when allowed', async () => {
    const w = mountCard()
    await flushPromises()
    expect(w.find('[data-testid="current-team"]').text()).toBe('Logística')
    expect(w.find('[data-testid="current-owner"]').text()).toBe('Ana')
    expect(w.find('[data-testid="transfer-history"]').text()).toContain('Dados completos')
    expect(w.find('[data-testid="transfer-forward"]').exists()).toBe(true)
    expect(w.find('[data-testid="transfer-return"]').exists()).toBe(true)
    svc.listTransfers.mockReturnValue(ok({ transfers: [], can_forward: false, can_return: false }))
    const w2 = mountCard()
    await flushPromises()
    expect(w2.find('[data-testid="transfer-forward"]').exists()).toBe(false)
    expect(w2.find('[data-testid="transfer-return"]').exists()).toBe(false)
  })

  it('forward needs destination and reason; never offers the current team; sends the chosen person', async () => {
    const w = mountCard()
    await flushPromises()
    await w.find('[data-testid="transfer-forward"]').trigger('click')
    await flushPromises()
    const confirm = () => document.querySelector('[data-testid="forward-confirm"]') as HTMLButtonElement
    expect(confirm().disabled).toBe(true)
    const sel = document.querySelector('[data-testid="forward-team"]') as HTMLSelectElement
    expect([...sel.options].map(o => o.text)).not.toContain('Logística')
    sel.value = 'tv'; sel.dispatchEvent(new Event('change'))
    await flushPromises()
    const person = document.querySelector('[data-testid="forward-person"]') as HTMLSelectElement
    person.value = 'u9'; person.dispatchEvent(new Event('change'))
    const reason = document.querySelector('[data-testid="forward-reason"]') as HTMLTextAreaElement
    reason.value = 'É venda'; reason.dispatchEvent(new Event('input'))
    await flushPromises()
    expect(confirm().disabled).toBe(false)
    confirm().click()
    await flushPromises()
    expect(svc.forward).toHaveBeenCalledWith('o1', { team_id: 'tv', assigned_user_id: 'u9', reason: 'É venda' })
    expect(w.emitted('changed')).toBeTruthy()
  })

  it('return names where it goes back to and demands a reason', async () => {
    const w = mountCard()
    await flushPromises()
    await w.find('[data-testid="transfer-return"]').trigger('click')
    await flushPromises()
    expect(document.body.textContent).toContain('goes back to SAC')
    const confirm = () => document.querySelector('[data-testid="return-confirm"]') as HTMLButtonElement
    expect(confirm().disabled).toBe(true)
    const reason = document.querySelector('[data-testid="return-reason"]') as HTMLTextAreaElement
    reason.value = 'Faltam dados'; reason.dispatchEvent(new Event('input'))
    await flushPromises()
    confirm().click()
    await flushPromises()
    expect(svc.returnCase).toHaveBeenCalledWith('o1', 'Faltam dados')
  })
})
