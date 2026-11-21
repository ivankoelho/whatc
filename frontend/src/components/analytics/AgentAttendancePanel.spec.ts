// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import pt from '@/i18n/locales/pt-BR.json'

const svc = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/services/api', () => ({
  agentAttendanceService: svc,
  teamsService: { list: () => Promise.resolve({ data: { teams: [{ id: 't1', name: 'Equipe Sul' }] } }) },
  unitsService: { list: () => Promise.resolve({ data: { data: { units: [] } } }) },
  rolesService: { list: () => Promise.resolve({ data: { roles: [] } }) },
}))
import AgentAttendancePanel from './AgentAttendancePanel.vue'

const i18n = createI18n({ legacy: false, locale: 'pt-BR', messages: { 'pt-BR': pt } })
const rows = [
  { user_id: 'u1', name: 'Ana', no_login_record: false, logins: 2, first_login: '2026-11-20T11:00:00Z', active_sessions: 1, connected_minutes: 125, away_minutes: 20, away_count: 1, open_away: true, disconnections: 1,
    sessions: [{ session_id: 's1', login_at: '2026-11-20T11:00:00Z', end_at: '2026-11-20T13:00:00Z', ended: 'manual', minutes: 120 }], away: [{ start_at: '2026-11-20T12:00:00Z', minutes: 20 }] },
  { user_id: 'u2', name: 'Bob', no_login_record: true, logins: 0, active_sessions: 0, connected_minutes: 0, away_minutes: 0, away_count: 0, open_away: false, disconnections: 0, sessions: [], away: [] },
]
const mountIt = () => mount(AgentAttendancePanel, { props: { from: '2026-11-20', to: '2026-11-20' }, global: { plugins: [i18n] } })

describe('AgentAttendancePanel', () => {
  beforeEach(() => { svc.get.mockReset(); svc.get.mockResolvedValue({ data: { data: { agents: rows } } }) })

  it('labels agents with no login as "Sem registro de login" (never "ausente")', async () => {
    const w = mountIt()
    await flushPromises()
    expect(w.find('[data-testid="no-login"]').text()).toBe('Sem registro de login')
    expect(w.text()).toContain('1 agente(s) sem registro de login')
  })

  it('flags an away period with no recorded return and expands the details', async () => {
    const w = mountIt()
    await flushPromises()
    expect(w.text()).toContain('sem retorno registrado')
    await w.findAll('[data-testid="attendance-row"]')[0].trigger('click')
    expect(w.text()).toContain('encerrada pelo agente')
  })

  it('sends the no-login filter to the API', async () => {
    const w = mountIt()
    await flushPromises()
    svc.get.mockClear()
    await w.find('[data-testid="only-no-login"]').setValue(true)
    await flushPromises()
    expect(svc.get).toHaveBeenCalledWith(expect.objectContaining({ only: 'no_login', from: '2026-11-20' }))
  })
})
