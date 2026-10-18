// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'

const api = vi.hoisted(() => ({
  channels: vi.fn(), archivedChannels: vi.fn(), users: vi.fn(), messages: vi.fn(), members: vi.fn(), adminChannels: vi.fn()
}))
vi.mock('@/services/api', () => ({ teamChatService: api }))
const perms = vi.hoisted(() => ({ granted: new Set<string>() }))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ user: { id: 'me' }, hasPermission: (r: string, a: string) => perms.granted.has(`${r}:${a}`) })
}))

import ChannelSidebar from './ChannelSidebar.vue'
import { useTeamChatStore } from '@/stores/teamChat'

const i18n = createI18n({ legacy: false, locale: 'en', messages: { en } })
const ScrollArea = { template: '<div><slot /></div>' }
const ok = (data: unknown) => Promise.resolve({ data: { data } })

const free = (id: string, name: string, extra: Record<string, unknown> = {}) => ({
  id, kind: 'channel', name, unread_count: 0, created_at: '2026-10-17T00:00:00Z',
  created_by_name: 'Ana', member_count: 3, my_role: 'owner', ...extra
})

function render(channels: unknown[], archived: unknown[] = []) {
  api.channels.mockReturnValue(ok({ channels }))
  api.archivedChannels.mockReturnValue(ok({ channels: archived }))
  const store = useTeamChatStore()
  const wrapper = mount(ChannelSidebar, {
    props: { activeId: null },
    global: { plugins: [i18n], stubs: { ScrollArea } }
  })
  return { store, wrapper }
}

beforeEach(() => {
  setActivePinia(createPinia())
  Object.values(api).forEach(f => f.mockReset())
  api.users.mockReturnValue(ok({ users: [] }))
  perms.granted = new Set(['team_chat:read', 'team_chat:write'])
})

describe('ChannelSidebar (phase A)', () => {
  it('same-named channels are told apart by creator and size, and only those', async () => {
    const { store, wrapper } = render([free('a', 'Sales'), free('b', 'sales', { created_by_name: 'Bruno', member_count: 8 }), free('c', 'Ops')])
    await store.fetchChannels()
    await flushPromises()

    expect(wrapper.get('[data-testid="homonym-a"]').text()).toBe('by Ana · 3 people')
    expect(wrapper.get('[data-testid="homonym-b"]').text()).toBe('by Bruno · 8 people')
    expect(wrapper.find('[data-testid="homonym-c"]').exists()).toBe(false)
  })

  it('archived channels stay out of the list until asked for, then show in their own section', async () => {
    const { store, wrapper } = render([free('a', 'Sales')], [free('z', 'Old project', { archived_at: '2026-10-01T00:00:00Z' })])
    await store.fetchChannels()
    await flushPromises()
    expect(wrapper.text()).not.toContain('Old project')
    expect(wrapper.get('[data-testid="toggle-archived"]').text()).toBe('Show archived channels')
    expect(wrapper.get('[data-testid="toggle-archived"]').attributes('aria-pressed')).toBe('false')

    await wrapper.get('[data-testid="toggle-archived"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('Archived')
    expect(wrapper.text()).toContain('Old project')
    expect(wrapper.get('[data-testid="toggle-archived"]').text()).toBe('Hide archived channels')
    expect(wrapper.get('[data-testid="toggle-archived"]').attributes('aria-pressed')).toBe('true')

    await wrapper.get('[data-testid="toggle-archived"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).not.toContain('Old project')
  })

  it('opening an archived channel selects it like any other', async () => {
    const { store, wrapper } = render([], [free('z', 'Old project', { archived_at: '2026-10-01T00:00:00Z' })])
    await store.fetchChannels()
    await store.setShowArchived(true)
    await flushPromises()
    await wrapper.get('[data-testid="channel-z"]').trigger('click')
    expect(wrapper.emitted('select')?.[0]).toEqual(['z'])
  })

  it('the administration shortcut and "new channel" need team_chat:manage', async () => {
    const plain = render([free('a', 'Sales')])
    await plain.store.fetchChannels()
    await flushPromises()
    expect(plain.wrapper.find('[data-testid="open-admin"]').exists()).toBe(false)
    expect(plain.wrapper.find('[aria-label="New channel"]').exists()).toBe(false)

    setActivePinia(createPinia())
    perms.granted = new Set(['team_chat:read', 'team_chat:write', 'team_chat:manage'])
    const manager = render([free('a', 'Sales')])
    await manager.store.fetchChannels()
    await flushPromises()
    expect(manager.wrapper.find('[aria-label="New channel"]').exists()).toBe(true)
    await manager.wrapper.get('[data-testid="open-admin"]').trigger('click')
    expect(manager.wrapper.emitted('admin')).toHaveLength(1)
  })
})
