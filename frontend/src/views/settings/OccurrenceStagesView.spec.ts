// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'

const svc = vi.hoisted(() => ({ listStages: vi.fn(), reorderStages: vi.fn(), updateStage: vi.fn(), createStage: vi.fn(), deleteStage: vi.fn() }))
vi.mock('@/services/api', async (orig) => ({ ...(await orig<object>()), occurrencesService: svc, teamsService: { list: vi.fn().mockResolvedValue({ data: { data: { teams: [] } } }) } }))
vi.mock('vue-sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import OccurrenceStagesView from './OccurrenceStagesView.vue'

const stage = (id: string, name: string, position: number, extra: Record<string, unknown> = {}) =>
  ({ id, name, position, color: '#888888', is_initial: false, is_closing: false, general_state: 'in_progress', ...extra })
const stages = [stage('a', 'Aberto', 0, { is_initial: true, general_state: 'new' }), stage('b', 'Em análise', 1), stage('c', 'Concluído', 2, { is_closing: true, general_state: 'resolved' })]

const i18n = createI18n({ legacy: false, locale: 'en', messages: { en } })
const btn = (w: ReturnType<typeof mount>, id: string, label: string) => w.find(`[data-testid="stage-row-${id}"] button[aria-label="${label}"]`)
const mountView = () => mount(OccurrenceStagesView, {
  global: { plugins: [createPinia(), i18n], stubs: { RouterLink: true, PageHeader: true, ScrollArea: { template: '<div><slot /></div>' }, Tooltip: { template: '<div><slot /></div>' }, TooltipTrigger: { template: '<div><slot /></div>' }, TooltipContent: true } },
})

describe('OccurrenceStagesView ordering', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    Object.values(svc).forEach(f => f.mockReset())
    svc.listStages.mockResolvedValue({ data: { data: { stages } } })
  })

  it('Move down sends the whole new order with the order the screen started from', async () => {
    svc.reorderStages.mockResolvedValue({ data: { data: {} } })
    const w = mountView()
    await flushPromises()
    await btn(w, 'a', 'Move down').trigger('click')
    await flushPromises()
    expect(svc.reorderStages).toHaveBeenCalledWith(['b', 'a', 'c'], ['a', 'b', 'c'])
    expect(w.find('[data-testid="stage-announce"]').text()).toContain('Aberto moved to position 2 of 3')
  })

  it('the first cannot go up and the last cannot go down', async () => {
    const w = mountView()
    await flushPromises()
    expect(btn(w, 'a', 'Move up').attributes('disabled')).toBeDefined()
    expect(btn(w, 'c', 'Move down').attributes('disabled')).toBeDefined()
  })

  it('a 409 (someone else reordered) reloads the current order instead of keeping the local guess', async () => {
    svc.reorderStages.mockRejectedValue({ response: { status: 409 } })
    const w = mountView()
    await flushPromises()
    svc.listStages.mockClear()
    await btn(w, 'c', 'Move up').trigger('click')
    await flushPromises()
    expect(svc.listStages).toHaveBeenCalled()
    const rows = w.findAll('[data-testid^="stage-row-"]').map(r => r.attributes('data-testid'))
    expect(rows).toEqual(['stage-row-a', 'stage-row-b', 'stage-row-c'])
  })
})
