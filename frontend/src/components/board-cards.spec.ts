// @vitest-environment happy-dom
// vuedraggable finds each Kanban item through the card component's root node.
// A comment (or anything) beside the root turns the card into a fragment and
// drag-and-drop silently stops working — keep both cards single-rooted.
import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'

vi.mock('@/services/api', () => ({ salesOpportunitiesService: {} }))
vi.mock('vue-sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import OccurrenceCard from './crm/OccurrenceCard.vue'
import SalesOpportunityCard from './sales/SalesOpportunityCard.vue'

const i18n = createI18n({ legacy: false, locale: 'en', messages: { en: {} }, missingWarn: false, fallbackWarn: false })
const global = {
  plugins: [i18n],
  stubs: { RouterLink: true, XProcessLinkForm: true, SalesOpportunityDetailsDialog: true, MoneyInput: true },
}

function expectSingleCardRoot(el: Node) {
  expect(el.nodeType).toBe(Node.ELEMENT_NODE)
  expect((el as Element).hasAttribute('data-board-card')).toBe(true)
}

describe('Kanban cards', () => {
  it('OccurrenceCard renders one root element', () => {
    const w = mount(OccurrenceCard, {
      props: {
        occurrence: {
          id: 'o1', protocol_number: '2026-000001', contact_id: 'c1', contact_name: 'Ana', title: 'Troca',
          priority: 'normal', opened_at: '2026-10-01T10:00:00Z',
        } as any,
      },
      global,
    })
    expectSingleCardRoot(w.element)
  })

  it('SalesOpportunityCard renders one root element', () => {
    const w = mount(SalesOpportunityCard, {
      props: {
        opportunity: {
          id: 's1', opportunity_number: 'OPP-1', contact_id: 'c1', stage: 'potencial', status: 'aberta',
          opened_at: '2026-10-01T10:00:00Z', stage_changed_at: '2026-10-01T10:00:00Z', sla_breached: false,
        } as any,
      },
      global,
    })
    expectSingleCardRoot(w.element)
  })
})
