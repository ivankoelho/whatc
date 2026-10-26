// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'

const svc = vi.hoisted(() => ({
  listParticipants: vi.fn(), supportDepartments: vi.fn(), supportUsers: vi.fn(), addParticipant: vi.fn(), removeParticipant: vi.fn(),
}))
vi.mock('@/services/api', () => ({ occurrencesService: svc }))
vi.mock('vue-sonner', () => ({ toast: { error: vi.fn() } }))

import OccurrenceParticipantsCard from './OccurrenceParticipantsCard.vue'

const i18n = createI18n({ legacy: false, locale: 'en', messages: { en } })
const row = (id: string, name: string, active: boolean, extra: Record<string, unknown> = {}) =>
  ({ user_id: id, user_name: name, capabilities: ['comment'], role: 'support', department_name: 'Logística', added_by: 'Ana', added_at: '2026-10-26T13:05:00Z', active, ...extra })
const ok = (data: unknown) => Promise.resolve({ data: { data } })

function setup(canWrite = true) {
  return mount(OccurrenceParticipantsCard, { props: { occurrenceId: 'o1', canWrite }, global: { plugins: [i18n] } })
}

describe('OccurrenceParticipantsCard (acompanhamento e apoio)', () => {
  beforeEach(() => {
    Object.values(svc).forEach(f => f.mockReset())
    svc.listParticipants.mockReturnValue(ok({ participants: [row('u1', 'Bia', true), row('u2', 'Caio', false, { removed_by: 'Ana', removed_at: '2026-10-26T14:00:00Z' })] }))
    svc.supportDepartments.mockReturnValue(ok({ departments: [{ id: 'd1', name: 'Logística' }] }))
    svc.supportUsers.mockReturnValue(ok({ users: [{ id: 'u1', full_name: 'Bia' }, { id: 'u3', full_name: 'Davi' }, { id: 'u4', full_name: 'Eva' }] }))
    svc.addParticipant.mockReturnValue(ok({}))
  })

  it('shows active links with department, who added and when; removed ones only in the history', async () => {
    const w = setup()
    await flushPromises()
    const text = w.text()
    expect(text).toContain('Bia')
    expect(text).toContain('Logística')
    expect(text).toContain('Added by Ana')
    expect(text).toMatch(/26\/10\/2026 \d{2}:05/) // DD/MM/YYYY HH:mm, 24h (the hour depends on the timezone)
    expect(w.find('[data-testid="participant-u2"]').exists()).toBe(false)
    expect(w.find('[data-testid="participant-history"]').text()).toContain('Removed by Ana')
  })

  it('loads only the chosen department members (minus those already linked) and adds the selected ones as support', async () => {
    const w = setup()
    await flushPromises()
    await w.find('[data-testid="support-department"]').setValue('d1')
    await flushPromises()
    expect(svc.supportUsers).toHaveBeenCalledWith('o1', 'd1')
    expect(w.find('[data-testid="support-user-u1"]').exists()).toBe(false) // already linked
    await w.find('[data-testid="support-user-u3"]').setValue(true)
    await w.find('[data-testid="support-user-u4"]').setValue(true)
    await w.find('[data-testid="participant-add"]').trigger('click')
    await flushPromises()
    expect(svc.addParticipant).toHaveBeenCalledTimes(2)
    expect(svc.addParticipant).toHaveBeenCalledWith('o1', 'u3', { role: 'support', departmentId: 'd1' })
  })

  it('offers no picker to someone who cannot write', async () => {
    const w = setup(false)
    await flushPromises()
    expect(svc.supportDepartments).not.toHaveBeenCalled()
    expect(w.find('[data-testid="support-department"]').exists()).toBe(false)
  })
})
