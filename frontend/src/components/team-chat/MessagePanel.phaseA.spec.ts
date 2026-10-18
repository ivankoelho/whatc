// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'
import ptBR from '@/i18n/locales/pt-BR.json'

const api = vi.hoisted(() => ({
  channels: vi.fn(), archivedChannels: vi.fn(), users: vi.fn(), messages: vi.fn(), members: vi.fn(),
  adminChannels: vi.fn(), markRead: vi.fn(), typing: vi.fn(), send: vi.fn()
}))
vi.mock('@/services/api', () => ({ teamChatService: api }))
vi.mock('vue-sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
const perms = vi.hoisted(() => ({ granted: new Set<string>() }))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ user: { id: 'me' }, hasPermission: (r: string, a: string) => perms.granted.has(`${r}:${a}`) })
}))

import MessagePanel from './MessagePanel.vue'
import { useTeamChatStore } from '@/stores/teamChat'

const ScrollArea = { template: '<div><slot /></div>' }
const ManageStub = { props: ['open', 'channelId'], template: '<div v-if="open" data-testid="manage-panel" />' }
const ok = (data: unknown = {}) => Promise.resolve({ data: { data } })

const sys = (id: string, meta: Record<string, string>) => ({
  id, channel_id: 'c1', user_id: 'ana', user_name: 'Ana', body: '', kind: 'system', meta,
  created_at: '2026-10-17T12:00:00Z'
})
const channel = (extra: Record<string, unknown> = {}) => ({
  id: 'c1', kind: 'channel', name: 'ops', unread_count: 0, created_at: '2026-10-17T00:00:00Z', ...extra
})

function render(opts: { channel?: Record<string, unknown>; messages?: unknown[]; locale?: 'en' | 'pt-BR' } = {}) {
  api.channels.mockReturnValue(ok({ channels: [opts.channel ?? channel()] }))
  api.archivedChannels.mockReturnValue(ok({ channels: [] }))
  const store = useTeamChatStore()
  const i18n = createI18n({ legacy: false, locale: opts.locale ?? 'en', messages: { en, 'pt-BR': ptBR } })
  const wrapper = mount(MessagePanel, {
    props: { channelId: 'c1' },
    global: { plugins: [i18n], stubs: { ScrollArea, ChannelManagePanel: ManageStub } }
  })
  store.panel('c1').messages = (opts.messages ?? []) as never
  return { store, wrapper }
}

beforeEach(() => {
  setActivePinia(createPinia())
  Object.values(api).forEach(f => f.mockReset())
  api.users.mockReturnValue(ok({ users: [] }))
  api.messages.mockReturnValue(ok({ messages: [], has_more: false }))
  api.members.mockReturnValue(ok({ members: [] }))
  api.typing.mockResolvedValue({})
  api.markRead.mockResolvedValue({})
  perms.granted = new Set(['team_chat:read', 'team_chat:write', 'team_chat:delete'])
  Element.prototype.scrollIntoView = vi.fn()
})

describe('system messages', () => {
  it('are written from the event and its parameters, as a centered note and not as a chat bubble', async () => {
    const { store, wrapper } = render({ messages: [
      sys('s1', { event: 'member_added', actor_name: 'Ana', target_name: 'Bia' }),
      sys('s2', { event: 'renamed', actor_name: 'Ana', old_name: 'ops', new_name: 'logistics' }),
      sys('s3', { event: 'member_left', actor_name: 'Caio' }),
      sys('s4', { event: 'archived', actor_name: 'Ana' })
    ] })
    await store.fetchChannels()
    await flushPromises()

    expect(wrapper.get('[data-testid="system-s1"]').text()).toBe('Ana added Bia.')
    expect(wrapper.get('[data-testid="system-s2"]').text()).toBe('Ana renamed the channel from "ops" to "logistics".')
    expect(wrapper.get('[data-testid="system-s3"]').text()).toBe('Caio left the channel.')
    expect(wrapper.get('[data-testid="system-s4"]').text()).toBe('Ana archived the channel.')
    expect(wrapper.find('[data-testid="message-s1"]').exists()).toBe(false)
  })

  it('are translated into the user language', async () => {
    const { store, wrapper } = render({ locale: 'pt-BR', messages: [sys('s1', { event: 'member_added', actor_name: 'Ana', target_name: 'Bia' })] })
    await store.fetchChannels()
    await flushPromises()
    expect(wrapper.get('[data-testid="system-s1"]').text()).toBe('Ana adicionou Bia.')
  })

  it('an administrative entry shows who entered and the reason, as plain text', async () => {
    const reason = 'Checking complaint <img src=x onerror="alert(1)"> <b>#1042</b>'
    const { store, wrapper } = render({ messages: [sys('s1', { event: 'admin_joined', actor_name: 'Carlos', reason })] })
    await store.fetchChannels()
    await flushPromises()

    const line = wrapper.get('[data-testid="system-s1"]')
    expect(line.text()).toBe(`Carlos entered as administrator. Reason: ${reason}`)
    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.find('b').exists()).toBe(false)
  })

  it('have no edit or delete controls, even for a moderator', async () => {
    const { store, wrapper } = render({ messages: [sys('s1', { event: 'member_added', actor_name: 'Me', target_name: 'Bia' })] })
    await store.fetchChannels()
    await flushPromises()
    expect(wrapper.find('[aria-label="Edit message"]').exists()).toBe(false)
    expect(wrapper.find('[aria-label="Delete message"]').exists()).toBe(false)
  })

  it('an unknown event is not shown as raw data', async () => {
    const { store, wrapper } = render({ messages: [sys('s1', { event: 'from_the_future', actor_name: 'Ana' })] })
    await store.fetchChannels()
    await flushPromises()
    expect(wrapper.get('[data-testid="system-s1"]').text()).toBe('')
    expect(wrapper.text()).not.toContain('from_the_future')
  })
})

describe('archived channel', () => {
  it('is read-only: a notice and a badge replace the composer, and reading stays', async () => {
    const { store, wrapper } = render({
      channel: channel({ archived_at: '2026-10-01T00:00:00Z' }),
      messages: [{ id: 'm1', channel_id: 'c1', user_id: 'ana', user_name: 'Ana', body: 'old news', created_at: '2026-10-01T00:00:00Z' }]
    })
    await store.fetchChannels()
    await flushPromises()

    expect(wrapper.find('textarea').exists()).toBe(false)
    expect(wrapper.text()).toContain(en.teamChat.manage.archivedNotice)
    expect(wrapper.text()).toContain('Archived')
    expect(wrapper.get('[data-testid="message-m1"]').text()).toContain('old news')
  })

  it('a normal channel keeps its composer', async () => {
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    expect(wrapper.find('textarea').exists()).toBe(true)
    expect(wrapper.text()).not.toContain(en.teamChat.manage.archivedNotice)
  })
})

describe('the management panel button', () => {
  it('exists for free channels only, never for team channels or DMs, and opens the panel', async () => {
    const free = render()
    await free.store.fetchChannels()
    await flushPromises()
    expect(free.wrapper.find('[data-testid="manage-panel"]').exists()).toBe(false)
    await free.wrapper.get('[aria-label="Channel members"]').trigger('click')
    expect(free.wrapper.find('[data-testid="manage-panel"]').exists()).toBe(true)

    for (const kind of ['team', 'dm']) {
      setActivePinia(createPinia())
      const other = render({ channel: channel({ kind }) })
      await other.store.fetchChannels()
      await flushPromises()
      expect(other.wrapper.find('[aria-label="Channel members"]').exists()).toBe(false)
    }
  })
})
