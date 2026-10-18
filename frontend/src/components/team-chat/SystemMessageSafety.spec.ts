// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'
import ptBR from '@/i18n/locales/pt-BR.json'

// What a person writes as the reason for an administrative entry (or a channel name)
// travels back to every member inside a system message. It must come out as TEXT:
// no element, no script, no attribute, no template or message-format evaluation.

const api = vi.hoisted(() => ({
  channels: vi.fn(), archivedChannels: vi.fn(), users: vi.fn(), messages: vi.fn(), members: vi.fn(),
  adminChannels: vi.fn(), markRead: vi.fn(), typing: vi.fn()
}))
vi.mock('@/services/api', () => ({ teamChatService: api }))
vi.mock('vue-sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ user: { id: 'me' }, hasPermission: () => true })
}))

import MessagePanel from './MessagePanel.vue'
import { useTeamChatStore } from '@/stores/teamChat'

const ScrollArea = { template: '<div><slot /></div>' }
const ManageStub = { props: ['open', 'channelId'], template: '<div />' }
const ok = (data: unknown = {}) => Promise.resolve({ data: { data } })

const PAYLOADS = [
  '<script>window.__xss = 1</script>',
  '<img src=x onerror="window.__xss = 1">',
  '<svg onload="window.__xss = 1"></svg>',
  '"><iframe src="javascript:window.__xss=1"></iframe>',
  '<a href="javascript:window.__xss=1">click me</a>',
  '<b>bold</b><i>italic</i><u>under</u>',
  '<style>*{display:none}</style>',
  '&lt;b&gt;already escaped&lt;/b&gt;',
  '{{ 7 * 7 }}',
  '${7 * 7}',
  '@:teamChat.title',
  '{actor} {target} {reason}',
  '‮evil right-to-left',
  '</p></div><div id="injected">x</div>'
]

function render(locale: 'en' | 'pt-BR', meta: Record<string, string>) {
  api.channels.mockReturnValue(ok({ channels: [{ id: 'c1', kind: 'channel', name: 'ops', unread_count: 0, created_at: '2026-10-17T00:00:00Z' }] }))
  api.archivedChannels.mockReturnValue(ok({ channels: [] }))
  const store = useTeamChatStore()
  const i18n = createI18n({ legacy: false, locale, messages: { en, 'pt-BR': ptBR } })
  const wrapper = mount(MessagePanel, {
    props: { channelId: 'c1' },
    global: { plugins: [i18n], stubs: { ScrollArea, ChannelManagePanel: ManageStub } },
    attachTo: document.body
  })
  store.panel('c1').messages = [{
    id: 's1', channel_id: 'c1', user_id: 'ana', user_name: 'Ana', body: '', kind: 'system', meta,
    created_at: '2026-10-17T12:00:00Z'
  }] as never
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
  Element.prototype.scrollIntoView = vi.fn()
  delete (window as unknown as Record<string, unknown>).__xss
  document.body.innerHTML = ''
})

describe.each(['en', 'pt-BR'] as const)('the reason of an administrative entry (%s)', locale => {
  it.each(PAYLOADS)('is shown as plain text: %s', async payload => {
    const { store, wrapper } = render(locale, { event: 'admin_joined', actor_name: 'Carlos', reason: payload })
    await store.fetchChannels()
    await flushPromises()

    const line = wrapper.get('[data-testid="system-s1"]')
    // Exactly the characters that were sent are on screen...
    expect(line.text()).toContain(payload)
    // ...and nothing was turned into markup or executed.
    expect(line.find('script, img, svg, iframe, a, b, i, u, style, #injected').exists()).toBe(false)
    expect(document.querySelector('#injected')).toBeNull()
    expect((window as unknown as Record<string, unknown>).__xss).toBeUndefined()
    // The line holds one text node: no child element at all.
    expect(line.get('p').element.children.length).toBe(0)
    wrapper.unmount()
  })
})

describe('other names that reach a system message', () => {
  it.each(PAYLOADS)('a channel name, person name and old/new name stay text: %s', async payload => {
    const { store, wrapper } = render('en', {
      event: 'renamed', actor_name: payload, old_name: payload, new_name: payload
    })
    await store.fetchChannels()
    await flushPromises()
    const line = wrapper.get('[data-testid="system-s1"]')
    expect(line.text()).toContain(payload)
    expect(line.get('p').element.children.length).toBe(0)
    expect((window as unknown as Record<string, unknown>).__xss).toBeUndefined()
    wrapper.unmount()
  })
})
