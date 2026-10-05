// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { h, reactive } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { TooltipProvider } from 'reka-ui'
import { createI18n } from 'vue-i18n'
import ptBR from '@/i18n/locales/pt-BR.json'
import en from '@/i18n/locales/en.json'

const route = reactive<{ query: Record<string, string> }>({ query: {} })
const router = vi.hoisted(() => ({ replace: vi.fn() }))
vi.mock('vue-router', () => ({ useRoute: () => route, useRouter: () => router }))

const perms = vi.hoisted(() => ({ granted: new Set<string>() }))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    organizationId: 'org-1',
    user: { id: 'u1', is_super_admin: false },
    hasPermission: (r: string, a: string) => perms.granted.has(`${r}:${a}`),
  }),
}))

const empty = vi.hoisted(() => () => Promise.resolve({ data: { data: {} } }))
vi.mock('@/services/api', () => ({
  organizationService: { getSettings: empty, updateSettings: empty },
  usersService: { me: empty, updateSettings: empty },
  brandingService: { getPublic: empty },
}))
vi.mock('vue-sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))
vi.mock('@/components/shared', async (orig) => ({ ...(await orig<any>()), AuditLogPanel: { render: () => null } }))
vi.mock('./UnitsView.vue', () => ({ default: { render: () => h('div', { 'data-testid': 'units-view' }) } }))
vi.mock('./DepartmentsView.vue', () => ({ default: { render: () => h('div', { 'data-testid': 'departments-view' }) } }))

import SettingsView from './SettingsView.vue'

let mounted: { unmount: () => void } | null = null
async function render() {
  const i18n = createI18n({ legacy: false, locale: 'pt-BR', messages: { 'pt-BR': ptBR as any, en: en as any } })
  const w = mount({ render: () => h(TooltipProvider, () => h(SettingsView)) }, {
    global: { plugins: [i18n], stubs: { RouterLink: true, LanguageSwitcher: true } },
    attachTo: document.body,
  })
  mounted = w
  await flushPromises()
  return w
}

const tabs = () => Array.from(document.body.querySelectorAll('[role="tab"]')).map(t => t.textContent!.trim())
const $ = (id: string) => document.body.querySelector(`[data-testid="${id}"]`)

afterEach(() => {
  mounted?.unmount()
  mounted = null
  document.body.innerHTML = ''
})

beforeEach(() => {
  vi.clearAllMocks()
  route.query = {}
  perms.granted = new Set(['settings.general:read', 'units:read', 'departments:read'])
})

describe('SettingsView tabs', () => {
  it('shows Geral | Notificações | Chamadas | Unidades | Departamentos in that order', async () => {
    await render()
    expect(tabs()).toEqual(['Geral', 'Notificações', 'Chamadas', 'Unidades', 'Departamentos'])
  })

  it('shows the units tab content for ?tab=units, without the general settings page', async () => {
    route.query = { tab: 'units' }
    await render()
    expect($('units-view')).not.toBeNull()
    expect($('departments-view')).toBeNull()
  })

  it('shows the departments tab content for ?tab=departments', async () => {
    route.query = { tab: 'departments' }
    await render()
    expect($('departments-view')).not.toBeNull()
  })

  it('hides Unidades and Departamentos without their read permission', async () => {
    perms.granted = new Set(['settings.general:read'])
    await render()
    expect(tabs()).toEqual(['Geral', 'Notificações', 'Chamadas'])
  })

  it('a user with only units:read lands on Unidades, not on a general tab they cannot read', async () => {
    perms.granted = new Set(['units:read'])
    await render()
    expect(tabs()).toEqual(['Unidades'])
    expect($('units-view')).not.toBeNull()
    expect(router.replace).toHaveBeenCalledWith({ query: { tab: 'units' } })
  })

  it('ignores ?tab=units when the user cannot read units', async () => {
    perms.granted = new Set(['settings.general:read'])
    route.query = { tab: 'units' }
    await render()
    expect($('units-view')).toBeNull()
    expect(router.replace).toHaveBeenCalledWith({ query: { tab: 'general' } })
  })
})
