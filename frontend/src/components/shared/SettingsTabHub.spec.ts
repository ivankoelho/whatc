// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { h, reactive } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { Users } from 'lucide-vue-next'

const route = reactive<{ query: Record<string, string> }>({ query: {} })
const router = vi.hoisted(() => ({ replace: vi.fn() }))
vi.mock('vue-router', () => ({ useRoute: () => route, useRouter: () => router }))

const auth = vi.hoisted(() => ({ granted: new Set<string>(), superAdmin: false }))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    user: { id: 'u1', get is_super_admin() { return auth.superAdmin } },
    hasPermission: (r: string, a: string) => auth.superAdmin || auth.granted.has(`${r}:${a}`),
  }),
}))

import SettingsTabHub, { type HubTab } from './SettingsTabHub.vue'

const panel = (id: string) => ({ render: () => h('div', { 'data-testid': id }) })
const TABS: HubTab[] = [
  { value: 'consumption', labelKey: 'Consumption', icon: Users, permission: 'whatsapp_usage', component: panel('consumption') },
  { value: 'rates', labelKey: 'Rates', icon: Users, permission: 'whatsapp_usage', superAdminOnly: true, component: panel('rates') },
  { value: 'units', labelKey: 'Units', icon: Users, permission: 'units', alsoNeeds: 'occurrences', component: panel('units') },
]

let mounted: { unmount: () => void } | null = null
async function render() {
  const i18n = createI18n({ legacy: false, locale: 'en', messages: { en: {} }, missingWarn: false, fallbackWarn: false })
  mounted = mount(SettingsTabHub, { props: { tabs: TABS, defaultTab: 'consumption' }, global: { plugins: [i18n] }, attachTo: document.body })
  await flushPromises()
}
const tabs = () => Array.from(document.body.querySelectorAll('[role="tab"]')).map(t => t.textContent!.trim())

afterEach(() => {
  mounted?.unmount()
  mounted = null
  document.body.innerHTML = ''
})
beforeEach(() => {
  vi.clearAllMocks()
  route.query = {}
  auth.superAdmin = false
  auth.granted = new Set(['whatsapp_usage:read', 'units:read', 'occurrences:read'])
})

describe('SettingsTabHub', () => {
  it('hides a superAdminOnly tab from everyone else, whatever their permissions', async () => {
    await render()
    expect(tabs()).toEqual(['Consumption', 'Units'])
  })

  it('shows it to a super admin', async () => {
    auth.superAdmin = true
    await render()
    expect(tabs()).toEqual(['Consumption', 'Rates', 'Units'])
  })

  it('a deep link to a hidden tab falls back to the default one', async () => {
    route.query = { tab: 'rates' }
    await render()
    expect(document.body.querySelector('[data-testid="rates"]')).toBeNull()
    expect(router.replace).toHaveBeenCalledWith({ query: { tab: 'consumption' } })
  })

  it('alsoNeeds hides a tab without the second permission (units lists answer under occurrences:read)', async () => {
    auth.granted = new Set(['whatsapp_usage:read', 'units:read'])
    await render()
    expect(tabs()).toEqual(['Consumption'])
  })
})
