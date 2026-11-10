// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'
import ptBR from '@/i18n/locales/pt-BR.json'

const svc = vi.hoisted(() => ({
  listAIAgents: vi.fn(), createAIAgent: vi.fn(), updateAIAgent: vi.fn(), deleteAIAgent: vi.fn(),
  getAIAgentReadiness: vi.fn(), getSettings: vi.fn(), updateSettings: vi.fn(),
}))
const accounts = vi.hoisted(() => ({ list: vi.fn() }))
vi.mock('@/services/api', async (orig) => ({ ...(await orig<object>()), chatbotService: svc, accountsService: accounts }))
vi.mock('vue-sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import AIAgentsView from './AIAgentsView.vue'

const agent = (over: Record<string, unknown> = {}) => ({
  id: 'ag-1', name: 'Agente Comercial', objective: 'Qualificar', instructions: '', company_info: '', tone: '',
  handoff_rules: '', is_enabled: true, whatsapp_accounts: ['loja-centro'], ...over,
})

const i18n = createI18n({ legacy: false, locale: 'en', messages: { en } })
const mountView = () => mount(AIAgentsView, {
  global: {
    plugins: [createPinia(), i18n],
    stubs: {
      RouterLink: true, PageHeader: { template: '<div><slot name="actions" /></div>' },
      ScrollArea: { template: '<div><slot /></div>' },
      // keep the real form fields but render the dialog inline and always open
      CrudFormDialog: { props: ['open'], emits: ['submit'], template: '<div v-if="open" data-testid="dialog"><slot /><button data-testid="submit" @click="$emit(\'submit\')">ok</button></div>' },
      DeleteConfirmDialog: true,
      IconButton: { props: ['label'], template: '<button type="button" :aria-label="label"></button>' },
      Select: { template: '<div><slot /></div>' }, SelectTrigger: true, SelectValue: true, SelectContent: true, SelectItem: true,
      Checkbox: { props: ['checked'], emits: ['update:checked'], template: '<input type="checkbox" :checked="checked" @change="$emit(\'update:checked\', $event.target.checked)" />' },
      Switch: { props: ['checked'], emits: ['update:checked'], template: '<input type="checkbox" :checked="checked" @change="$emit(\'update:checked\', $event.target.checked)" />' },
    },
  },
})

describe('AIAgentsView', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    Object.values(svc).forEach(f => f.mockReset())
    accounts.list.mockReset()
    svc.listAIAgents.mockResolvedValue({ data: { data: { agents: [agent()], total: 1 } } })
    svc.getSettings.mockResolvedValue({ data: { data: { settings: { ai_default_agent_id: 'ag-1', ai_monthly_token_limit: 250000 } } } })
    svc.getAIAgentReadiness.mockResolvedValue({ data: { data: { issues: [] } } })
    accounts.list.mockResolvedValue({ data: { data: { accounts: [{ name: 'loja-centro' }, { name: 'loja-norte' }] } } })
  })

  it('lists the agents with the numbers they are allowed on and the saved cap', async () => {
    const w = mountView()
    await flushPromises()
    const row = w.find('[data-testid="agent-row-ag-1"]')
    expect(row.text()).toContain('Agente Comercial')
    expect(row.text()).toContain('loja-centro')
    expect((w.find('[data-testid="monthly-limit"]').element as HTMLInputElement).value).toBe('250000')
  })

  it('warns, per allowed number, when the unit team is missing', async () => {
    svc.getAIAgentReadiness.mockResolvedValue({ data: { data: { issues: [{ account: 'loja-centro', problem: 'team_without_unit' }] } } })
    const w = mountView()
    await flushPromises()
    expect(w.find('[data-testid="agent-issue"]').text()).toContain("loja-centro: the number's default team has no unit")
  })

  it('creates an agent with the fields and only the ticked numbers', async () => {
    svc.createAIAgent.mockResolvedValue({ data: { data: { id: 'new' } } })
    const w = mountView()
    await flushPromises()

    await w.find('[data-testid="new-agent"]').trigger('click')
    expect(w.find('[data-testid="no-accounts-warning"]').exists()).toBe(true) // nothing ticked yet
    await w.find('[data-testid="field-name"]').setValue('Novo')
    await w.find('[data-testid="field-display-name"]').setValue('Atendente Virtual Loja Centro')
    await w.find('[data-testid="field-objective"]').setValue('Qualificar compras')
    await w.find('[data-testid="field-handoff"]').setValue('Transferir se pedir humano')
    await w.find('[data-testid="account-loja-norte"]').setValue(true)
    await w.find('[data-testid="field-enabled"]').setValue(true)
    await w.find('[data-testid="submit"]').trigger('click')
    await flushPromises()

    expect(svc.createAIAgent).toHaveBeenCalledWith(expect.objectContaining({
      name: 'Novo', display_name: 'Atendente Virtual Loja Centro', objective: 'Qualificar compras', handoff_rules: 'Transferir se pedir humano',
      is_enabled: true, whatsapp_accounts: ['loja-norte'],
    }))
  })

  it('does not submit without a name', async () => {
    const w = mountView()
    await flushPromises()
    await w.find('[data-testid="new-agent"]').trigger('click')
    await w.find('[data-testid="submit"]').trigger('click')
    await flushPromises()
    expect(svc.createAIAgent).not.toHaveBeenCalled()
  })

  it('edits only what the form sends and toggles an agent on and off', async () => {
    svc.updateAIAgent.mockResolvedValue({ data: { data: {} } })
    const w = mountView()
    await flushPromises()

    await w.find('[data-testid="toggle-ag-1"]').setValue(false)
    await flushPromises()
    expect(svc.updateAIAgent).toHaveBeenCalledWith('ag-1', { is_enabled: false })
  })

  it('saves the default agent (empty string clears it) and the cap as a whole number', async () => {
    svc.updateSettings.mockResolvedValue({ data: { data: {} } })
    const w = mountView()
    await flushPromises()

    await w.find('[data-testid="monthly-limit"]').setValue('1500.7')
    await w.find('[data-testid="save-settings"]').trigger('click')
    await flushPromises()
    expect(svc.updateSettings).toHaveBeenCalledWith({ ai_default_agent_id: 'ag-1', ai_monthly_token_limit: 1500 })
  })

  it('has the screen strings in both languages', () => {
    const keys = (o: Record<string, unknown>, p = ''): string[] =>
      Object.entries(o).flatMap(([k, v]) => (v && typeof v === 'object' ? keys(v as Record<string, unknown>, `${p}${k}.`) : [`${p}${k}`]))
    const enKeys = keys((en as any).aiAgents).sort()
    const ptKeys = keys((ptBR as any).aiAgents).sort()
    expect(ptKeys).toEqual(enKeys)
    expect((en as any).nav.aiAgents).toBeTruthy()
    expect((ptBR as any).nav.aiAgents).toBeTruthy()
  })
})
