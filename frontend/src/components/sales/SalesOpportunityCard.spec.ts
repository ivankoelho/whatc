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
  global: { plugins: [i18n], stubs: { RouterLink: true, XProcessLinkForm: true, SalesOpportunityDetailsDialog: true, MoneyInput: true } },
})

const QUAL = 'Qualificação feita pelo agente de IA.\n\nInformado pelo cliente:\n- Área do direito: Trabalhista\n- Cidade: Salvador\n- Urgência: audiência na sexta\n- Extra: não cabe\n\nAinda não coletado:\n- Telefone alternativo\n\nReunião agendada: 24/11/2026 às 09:00.'

describe('SalesOpportunityCard (what the AI agent and the ads bring)', () => {
  it('shows the AI mark, the booked meeting, the ad and the qualification of an AI-opened opportunity', () => {
    const start = new Date(Date.now() + 3 * 24 * 3600 * 1000).toISOString()
    const w = mountCard({
      created_by_ai: true, interest: QUAL,
      meeting: { start, location: 'Escritório Centro' }, ad_origin: { headline: 'Consulta jurídica gratuita', clicks: 2 },
    })
    expect(w.find('[data-testid="sales-opportunity-ai-badge"]').exists()).toBe(true)
    expect(w.find('[data-testid="sales-opportunity-meeting"]').attributes('title')).toBe('Escritório Centro')
    expect(w.find('[data-testid="sales-opportunity-ad-origin"]').text()).toContain('Consulta jurídica gratuita')
    const lines = w.findAll('[data-testid="sales-opportunity-qualification"] li').map(l => l.text())
    expect(lines).toEqual(['Área do direito: Trabalhista', 'Cidade: Salvador', 'Urgência: audiência na sexta'])
    expect(w.text()).not.toContain('Ainda não coletado')
  })

  it('keeps the store block (value, direcionamento) out of an untouched AI card, but not out of a store card', () => {
    const ai = mountCard({ created_by_ai: true, interest: QUAL })
    expect(ai.find('[data-testid="sales-opportunity-value"]').exists()).toBe(false)
    expect(ai.find('[data-testid="sales-opportunity-direcionamento-select"]').exists()).toBe(false)
    expect(ai.text()).toContain('OPP-20261122-000001') // the number is still there

    const store = mountCard({})
    expect(store.find('[data-testid="sales-opportunity-value"]').exists()).toBe(true)
    expect(store.find('[data-testid="sales-opportunity-ai-badge"]').exists()).toBe(false)
  })

  it('shows the store block of an AI card once somebody has worked on it', () => {
    expect(mountCard({ created_by_ai: true, estimated_value: 1500 }).find('[data-testid="sales-opportunity-value"]').exists()).toBe(true)
    expect(mountCard({ created_by_ai: true, stage: 'abrir_orcamento' }).find('[data-testid="sales-opportunity-value"]').exists()).toBe(true)
    expect(mountCard({ created_by_ai: true, direcionamento: 'visita' }).find('[data-testid="sales-opportunity-value"]').exists()).toBe(true)
  })

  it('shows plain interest text as its first line, and nothing for an empty one', () => {
    expect(mountCard({ interest: 'Quer piso para a sala\nsegunda linha' }).find('[data-testid="sales-opportunity-qualification"] li').text()).toBe('Quer piso para a sala')
    expect(mountCard({ interest: '' }).find('[data-testid="sales-opportunity-qualification"]').exists()).toBe(false)
    expect(mountCard({ interest: 'Atendimento qualificado pelo agente de IA.\nQualificação não extraída: confira a conversa.' }).find('[data-testid="sales-opportunity-qualification"] li').text())
      .toBe('Atendimento qualificado pelo agente de IA.')
  })

  it('marks a meeting today differently', () => {
    const w = mountCard({ meeting: { start: new Date().toISOString() } })
    expect(w.find('[data-testid="sales-opportunity-meeting"]').classes().join(' ')).toContain('emerald')
  })

  it('has the card strings in both languages', async () => {
    const pt = (await import('@/i18n/locales/pt-BR.json')).default as unknown as { sales: Record<string, string> }
    for (const k of ['aiBadge', 'aiBadgeTitle', 'adOriginTitle', 'adOriginUnnamed', 'meetingsTodayTitle', 'quickFilters', 'quickHasMeeting', 'quickFromAd', 'quickAI', 'quickUnassigned']) {
      expect((en as unknown as { sales: Record<string, string> }).sales[k], `en ${k}`).toBeTruthy()
      expect(pt.sales[k], `pt ${k}`).toBeTruthy()
    }
  })
})
