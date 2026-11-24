// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import pt from '@/i18n/locales/pt-BR.json'

const svc = vi.hoisted(() => ({ list: vi.fn(), markSeen: vi.fn(), dismiss: vi.fn(), dismissAll: vi.fn() }))
vi.mock('@/services/api', () => ({ occurrenceAlertsService: svc }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
const perms = vi.hoisted(() => ({ canRead: true }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ hasPermission: () => perms.canRead }) }))

import HeaderTools from './HeaderTools.vue'

const i18n = createI18n({ legacy: false, locale: 'pt-BR', messages: { 'pt-BR': pt } })
const ok = (data: unknown) => Promise.resolve({ data: { data } })
const alert = (o: object = {}) => ({ id: 'a1', kind: 'assigned', created_at: '2026-11-20T12:00:00Z', occurrence_id: 'o1', protocol_number: '2026-000001',
  title: 'Pneu', contact_name: 'Cliente', what_happened: 'Ocorrência com Caminhão', priority: 'high', opened_at: '2026-11-20T12:00:00Z', ...o })
const mounted: ReturnType<typeof mount>[] = []
const mountIt = () => { const w = mount(HeaderTools, { global: { plugins: [i18n] }, attachTo: document.body }); mounted.push(w); return w }

describe('HeaderTools', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] })
    vi.setSystemTime(new Date('2026-11-20T23:38:16Z')) // 20:38:16 in São Paulo (UTC-3)
    Object.values(svc).forEach(f => f.mockReset())
    svc.list.mockReturnValue(ok({ alerts: [alert(), alert({ id: 'a2', seen_at: '2026-11-20T12:30:00Z', what_happened: undefined })], unseen: 1 }))
    svc.dismiss.mockReturnValue(Promise.resolve({}))
    perms.canRead = true
  })
  afterEach(() => { mounted.splice(0).forEach(w => w.unmount()); vi.useRealTimers(); document.body.innerHTML = '' })

  it('badges only the unseen alerts, not every open protocol', async () => {
    const w = mountIt()
    await flushPromises()
    expect(w.find('[data-testid="bell"]').text()).toBe('1')
  })

  it('hides the bell without occurrences:read', async () => {
    perms.canRead = false
    const w = mountIt()
    await flushPromises()
    expect(w.find('[data-testid="bell"]').exists()).toBe(false)
    expect(svc.list).not.toHaveBeenCalled()
  })

  it('refetches when the server pushes an alert', async () => {
    mountIt()
    await flushPromises()
    svc.list.mockClear()
    window.dispatchEvent(new CustomEvent('occurrence-alert'))
    await flushPromises()
    expect(svc.list).toHaveBeenCalledTimes(1)
  })
})
