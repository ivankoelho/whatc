// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'

const svc = vi.hoisted(() => ({ updateDetails: vi.fn(), changeDirecionamento: vi.fn(), listEvents: vi.fn() }))
const x2 = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/services/api', () => ({ salesOpportunitiesService: svc, salesFeaturesService: x2 }))
const toast = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn() }))
vi.mock('vue-sonner', () => ({ toast }))

import SalesOpportunityDialog from './SalesOpportunityDialog.vue'

const i18n = createI18n({ legacy: false, locale: 'en', messages: { en }, missingWarn: false, fallbackWarn: false })
const pass = { template: '<div><slot /></div>' }
const ok = (data: unknown) => Promise.resolve({ data: { data } })
const base = (over: Record<string, unknown> = {}) => ({
  id: 's1', opportunity_number: 'OPP-1', contact_id: 'c1', stage: 'potencial', status: 'aberta', interest: 'Quer piso',
  opened_at: '2026-11-22T10:00:00Z', stage_changed_at: '2026-11-22T10:00:00Z', sla_breached: false,
  contact: { id: 'c1', profile_name: 'Maria Silva', phone_number: '5571988887777' }, ...over,
})
const mountIt = (opp: Record<string, unknown>, open = false) => mount(SalesOpportunityDialog, {
  props: { open, opportunity: opp as never },
  global: {
    plugins: [i18n],
    stubs: {
      Dialog: { props: ['open'], template: '<div v-if="open"><slot /></div>' }, DialogContent: pass, DialogHeader: pass, DialogTitle: pass, DialogDescription: pass,
      RouterLink: { template: '<a><slot /></a>' }, XProcessLinkForm: { template: '<div data-testid="x2-form" />' },
      MoneyInput: { props: ['modelValue'], emits: ['update:modelValue'], template: '<input :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />' },
      Select: { template: '<div><slot /></div>' }, SelectTrigger: true, SelectValue: true, SelectContent: true, SelectItem: true,
    },
  },
})
const openIt = async (w: ReturnType<typeof mountIt>) => { await w.setProps({ open: true }); await flushPromises() }

describe('SalesOpportunityDialog', () => {
  beforeEach(() => {
    Object.values(svc).forEach(f => { f.mockReset() })
    x2.get.mockReset()
    x2.get.mockResolvedValue({ data: { data: { xprocess: true, direcionamento: true } } })
    toast.error.mockReset()
    toast.success.mockReset()
    svc.listEvents.mockReturnValue(ok({ events: [{ id: 'e1', type: 'opened', created_at: '2026-11-22T10:00:00Z', source: 'system' }, { id: 'e2', type: 'stage_changed', created_at: '2026-11-22T11:00:00Z', source: 'manual' }] }))
    svc.updateDetails.mockImplementation((_id: string, d: Record<string, unknown>) => ok({ ...base(), ...d }))
  })

  it('shows everything the compact card leaves out: the qualification, the meeting, the ad, the facts and the history', async () => {
    const start = new Date(Date.now() + 2 * 24 * 3600 * 1000).toISOString()
    const w = mountIt(base({ created_by_ai: true, meeting: { start, location: 'Escritório Centro' }, ad_origin: { headline: 'Consulta jurídica', clicks: 2 },
      assigned_user: { id: 'u', full_name: 'Ana Souza' } }))
    await openIt(w)
    expect((w.find('[data-testid="dialog-interest"]').element as HTMLTextAreaElement).value).toBe('Quer piso')
    expect(w.find('[data-testid="dialog-ai-badge"]').exists()).toBe(true)
    expect(w.find('[data-testid="dialog-meeting"]').text()).toContain('Escritório Centro')
    expect(w.find('[data-testid="dialog-ad-origin"]').text()).toContain('Consulta jurídica')
    expect(w.find('[data-testid="dialog-facts"]').text()).toContain('Ana Souza')
    expect(w.find('[data-testid="dialog-stage"]').text()).toBe('Potential')
    expect(svc.listEvents).toHaveBeenCalledWith('s1')
    expect(w.find('[data-testid="dialog-events"]').text()).toContain('Opportunity opened')
    expect(w.find('[data-testid="dialog-events"]').text()).toContain('Changed stage')
  })

  it('fills itself when it is mounted already open, which is how the board uses it', async () => {
    const w = mountIt(base({ interest: 'Trabalhista, Salvador' }), true)
    await flushPromises()
    expect((w.find('[data-testid="dialog-interest"]').element as HTMLTextAreaElement).value).toBe('Trabalhista, Salvador')
    expect(svc.listEvents).toHaveBeenCalledWith('s1')
    expect(w.find('[data-testid="dialog-events"]').exists()).toBe(true)
  })

  it('saves only what changed', async () => {
    const w = mountIt(base({ estimated_value: 100 }))
    await openIt(w)
    await w.find('[data-testid="dialog-interest"]').setValue('Trabalhista, Salvador')
    await w.find('[data-testid="dialog-save"]').trigger('click')
    await flushPromises()
    expect(svc.updateDetails).toHaveBeenCalledTimes(1)
    expect(svc.updateDetails).toHaveBeenCalledWith('s1', { interest: 'Trabalhista, Salvador' })
    expect(w.emitted('saved')).toHaveLength(1)
    expect(w.emitted('update:open')?.at(-1)).toEqual([false])
  })

  it('closes without a request when nothing changed', async () => {
    const w = mountIt(base())
    await openIt(w)
    await w.find('[data-testid="dialog-save"]').trigger('click')
    await flushPromises()
    expect(svc.updateDetails).not.toHaveBeenCalled()
    expect(w.emitted('update:open')?.at(-1)).toEqual([false])
  })

  it('rejects an invalid number before asking the server', async () => {
    const w = mountIt(base())
    await openIt(w)
    await w.find('[data-testid="dialog-quantity"]').setValue('abc')
    await w.find('[data-testid="dialog-save"]').trigger('click')
    await flushPromises()
    expect(svc.updateDetails).not.toHaveBeenCalled()
    expect(toast.error).toHaveBeenCalled()
  })

  it('edits what actually happened on a converted opportunity, and not the interest', async () => {
    const w = mountIt(base({ status: 'convertida', realized_value: 500 }))
    await openIt(w)
    expect((w.find('[data-testid="dialog-interest"]').element as HTMLTextAreaElement).disabled).toBe(true)
    expect(w.find('[data-testid="dialog-value"]').exists()).toBe(false)
    await w.find('[data-testid="dialog-realized-value"]').setValue('650')
    await w.find('[data-testid="dialog-save"]').trigger('click')
    await flushPromises()
    expect(svc.updateDetails).toHaveBeenCalledWith('s1', { realized_value: 650 })
  })

  it('offers convert and lose only for an open opportunity in direcionada, and hands them to the board', async () => {
    let w = mountIt(base())
    await openIt(w)
    expect(w.find('[data-testid="dialog-convert"]').exists()).toBe(false)

    w = mountIt(base({ stage: 'direcionada', direcionamento: 'visita' }))
    await openIt(w)
    await w.find('[data-testid="dialog-convert"]').trigger('click')
    await w.find('[data-testid="dialog-lose"]').trigger('click')
    expect(w.emitted('convert')).toHaveLength(1)
    expect(w.emitted('lose')).toHaveLength(1)
  })

  it('hides the X2 order field when the organization has no X2 integration', async () => {
    let w = mountIt(base())
    await openIt(w)
    expect(w.find('[data-testid="x2-form"]').exists()).toBe(true)

    x2.get.mockResolvedValue({ data: { data: { xprocess: false, direcionamento: true } } })
    w = mountIt(base())
    await openIt(w)
    expect(w.find('[data-testid="x2-form"]').exists()).toBe(false)
    expect(w.find('[data-testid="dialog-direcionamento"]').exists()).toBe(true)
  })

  it('hides direcionamento for an organization that turned it off, and keeps the rest', async () => {
    x2.get.mockResolvedValue({ data: { data: { xprocess: true, direcionamento: false } } })
    const w = mountIt(base())
    await openIt(w)
    expect(w.find('[data-testid="dialog-direcionamento"]').exists()).toBe(false)
    expect(w.find('[data-testid="x2-form"]').exists()).toBe(true)
    expect(w.find('[data-testid="dialog-task-at"]').exists()).toBe(true)
  })

  it('lets an organization without direcionamento convert or lose from any open stage', async () => {
    x2.get.mockResolvedValue({ data: { data: { xprocess: false, direcionamento: false } } })
    const w = mountIt(base())
    await openIt(w)
    expect(w.find('[data-testid="dialog-convert"]').exists()).toBe(true)
    expect(w.find('[data-testid="dialog-lose"]').exists()).toBe(true)
  })

  it('schedules the next task, edits its note and clears it', async () => {
    const w = mountIt(base())
    await openIt(w)
    await w.find('[data-testid="dialog-task-at"]').setValue('2030-05-04T10:30')
    await w.find('[data-testid="dialog-task-note"]').setValue('Ligar com o orçamento')
    await w.find('[data-testid="dialog-save"]').trigger('click')
    await flushPromises()
    const sent = svc.updateDetails.mock.calls[0][1] as Record<string, string>
    expect(new Date(sent.next_task_at).getTime()).toBe(new Date('2030-05-04T10:30').getTime())
    expect(sent.next_task_note).toBe('Ligar com o orçamento')

    svc.updateDetails.mockClear()
    const at = new Date('2030-05-04T10:30').toISOString()
    const w2 = mountIt(base({ next_task_at: at, next_task_note: 'Ligar' }))
    await openIt(w2)
    await w2.find('[data-testid="dialog-task-at"]').setValue('')
    await w2.find('[data-testid="dialog-save"]').trigger('click')
    await flushPromises()
    expect(svc.updateDetails).toHaveBeenCalledWith('s1', { next_task_at: '' })
  })

  it('does not break when the history cannot be loaded', async () => {
    svc.listEvents.mockImplementation(() => Promise.reject({ response: { status: 500 } }))
    const w = mountIt(base())
    await openIt(w)
    expect(w.text()).toContain('No events recorded')
    expect(w.find('[data-testid="dialog-save"]').exists()).toBe(true)
  })
})
