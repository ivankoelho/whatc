// @vitest-environment happy-dom
import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'

vi.mock('@/services/api', () => ({ salesOpportunitiesService: {} }))
vi.mock('vue-sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import SalesOpportunityCard from './SalesOpportunityCard.vue'

const i18n = createI18n({ legacy: false, locale: 'en', messages: { en }, missingWarn: false, fallbackWarn: false })
const mountCard = (over: Record<string, unknown> = {}) => mount(SalesOpportunityCard, {
  props: {
    opportunity: {
      id: 's1', opportunity_number: 'OPP-20261122-000001', contact_id: 'c1', stage: 'potencial', status: 'aberta',
      opened_at: '2026-11-22T10:00:00Z', stage_changed_at: '2026-11-22T10:00:00Z', sla_breached: false,
      contact: { id: 'c1', profile_name: 'Maria Silva', phone_number: '5571988887777' }, ...over,
    } as never,
  },
  global: { plugins: [i18n], stubs: { RouterLink: { template: '<a data-testid="chat-link"><slot /></a>' } } },
})

const QUAL = 'Qualificação feita pelo agente de IA.\n\nInformado pelo cliente:\n- Área do direito: Trabalhista\n- Cidade: Salvador\n- Urgência: audiência na sexta\n\nAinda não coletado:\n- Telefone alternativo\n\nReunião agendada: 24/11/2026 às 09:00.'

describe('SalesOpportunityCard (compact, opens the popup)', () => {
  it('shows who, what they want in one line, the meeting, the ad, and the AI mark', () => {
    const start = new Date(Date.now() + 3 * 24 * 3600 * 1000).toISOString()
    const w = mountCard({
      created_by_ai: true, interest: QUAL,
      meeting: { start, location: 'Escritório Centro' }, ad_origin: { headline: 'Consulta jurídica gratuita', clicks: 2 },
    })
    expect(w.text()).toContain('Maria Silva')
    expect(w.find('[data-testid="sales-opportunity-ai-badge"]').exists()).toBe(true)
    expect(w.find('[data-testid="sales-opportunity-summary"]').text()).toBe('Área do direito: Trabalhista · Cidade: Salvador')
    expect(w.find('[data-testid="sales-opportunity-meeting"]').attributes('title')).toBe('Escritório Centro')
    expect(w.find('[data-testid="sales-opportunity-ad-origin"]').attributes('title')).toContain('Consulta jurídica gratuita')
    expect(w.text()).not.toContain('Ainda não coletado')
  })

  it('has no inline editors any more: those live in the popup', () => {
    const w = mountCard({ estimated_value: 1500, direcionamento: 'visita' })
    expect(w.find('input').exists()).toBe(false)
    expect(w.find('select, [role="combobox"]').exists()).toBe(false)
    expect(w.find('button').exists()).toBe(false)
  })

  it('shows the value only when there is one, and a "?" seller when nobody owns it', () => {
    expect(mountCard({}).find('[data-testid="sales-opportunity-value"]').exists()).toBe(false)
    expect(mountCard({ estimated_value: 1500 }).find('[data-testid="sales-opportunity-value"]').text()).toContain('1.500')
    expect(mountCard({ assigned_user: { id: 'u', full_name: 'Ana Souza' } }).text()).toContain('AS')
    expect(mountCard({}).text()).toContain('?')
  })

  it('shows plain interest text as its first line, and nothing for an empty one', () => {
    expect(mountCard({ interest: 'Quer piso para a sala\nsegunda linha' }).find('[data-testid="sales-opportunity-summary"]').text()).toBe('Quer piso para a sala')
    expect(mountCard({ interest: '' }).find('[data-testid="sales-opportunity-summary"]').exists()).toBe(false)
    expect(mountCard({ interest: 'Atendimento qualificado pelo agente de IA.\nQualificação não extraída: confira a conversa.' }).find('[data-testid="sales-opportunity-summary"]').text())
      .toBe('Atendimento qualificado pelo agente de IA.')
  })

  it('marks a meeting today and an SLA breach', () => {
    expect(mountCard({ meeting: { start: new Date().toISOString() } }).find('[data-testid="sales-opportunity-meeting"]').classes().join(' ')).toContain('emerald')
    expect(mountCard({ sla_breached: true }).find('[data-testid="sales-opportunity-sla"]').exists()).toBe(true)
  })

  it('opens the popup on click and on Enter, but not when the conversation link is clicked', async () => {
    const w = mountCard({})
    await w.trigger('click')
    await w.trigger('keydown.enter')
    expect(w.emitted('open')).toHaveLength(2)
    expect((w.emitted('open')![0][0] as { id: string }).id).toBe('s1')
    await w.find('[data-testid="chat-link"]').trigger('click')
    expect(w.emitted('open')).toHaveLength(2)
  })

  it('has the card strings in both languages', async () => {
    const pt = (await import('@/i18n/locales/pt-BR.json')).default as unknown as { sales: Record<string, string> }
    for (const k of ['aiBadge', 'aiBadgeTitle', 'adOriginTitle', 'adOriginUnnamed', 'meetingsTodayTitle', 'quickFilters', 'quickHasMeeting', 'quickFromAd', 'quickAI', 'quickUnassigned', 'seller', 'openedAt', 'stageChangedAt', 'phone', 'history', 'historyEmpty', 'meetingLabel']) {
      expect((en as unknown as { sales: Record<string, string> }).sales[k], `en ${k}`).toBeTruthy()
      expect(pt.sales[k], `pt ${k}`).toBeTruthy()
    }
  })
})
