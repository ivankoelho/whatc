// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'
import ptBR from '@/i18n/locales/pt-BR.json'

const api = vi.hoisted(() => ({
  channels: vi.fn(), archivedChannels: vi.fn(), users: vi.fn(), messages: vi.fn(), members: vi.fn(),
  adminChannels: vi.fn(), markRead: vi.fn(), typing: vi.fn(), send: vi.fn(), react: vi.fn(), unreact: vi.fn(), message: vi.fn()
}))
vi.mock('@/services/api', () => ({ teamChatService: api }))
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock('vue-sonner', () => ({ toast }))
const perms = vi.hoisted(() => ({ granted: new Set<string>() }))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ user: { id: 'me' }, hasPermission: (r: string, a: string) => perms.granted.has(`${r}:${a}`) })
}))

import MessagePanel from './MessagePanel.vue'
import { useTeamChatStore } from '@/stores/teamChat'
import { TEAM_CHAT_REACTIONS } from '@/lib/team-chat'

const ScrollArea = { template: '<div><slot /></div>' }
const ManageStub = { props: ['open', 'channelId'], template: '<div />' }
const ok = (data: unknown = {}) => Promise.resolve({ data: { data } })
const fail = (status: number, code: string) => Promise.reject({ response: { status, data: { error_type: code } } })

const msg = (id: string, from: string, body: string, extra: Record<string, unknown> = {}) => ({
  id, channel_id: 'c1', user_id: from, user_name: from === 'me' ? 'Me' : 'Ana', body, kind: 'user',
  created_at: '2026-10-17T12:00:00Z', reactions: [], ...extra
})

function render(opts: { archived?: boolean; messages?: unknown[]; locale?: 'en' | 'pt-BR' } = {}) {
  api.channels.mockReturnValue(ok({ channels: [{
    id: 'c1', kind: 'channel', name: 'ops', unread_count: 0, created_at: '2026-10-17T00:00:00Z',
    archived_at: opts.archived ? '2026-10-17T10:00:00Z' : undefined
  }] }))
  api.archivedChannels.mockReturnValue(ok({ channels: [] }))
  const store = useTeamChatStore()
  const i18n = createI18n({ legacy: false, locale: opts.locale ?? 'en', messages: { en, 'pt-BR': ptBR } })
  const wrapper = mount(MessagePanel, {
    props: { channelId: 'c1' },
    global: { plugins: [i18n], stubs: { ScrollArea, ChannelManagePanel: ManageStub } },
    attachTo: document.body
  })
  store.panel('c1').messages = (opts.messages ?? []) as never
  return { store, wrapper }
}

beforeEach(() => {
  setActivePinia(createPinia())
  Object.values(api).forEach(f => f.mockReset())
  toast.error.mockReset()
  api.users.mockReturnValue(ok({ users: [] }))
  api.messages.mockReturnValue(ok({ messages: [], has_more: false }))
  api.members.mockReturnValue(ok({ members: [] }))
  api.typing.mockResolvedValue({})
  api.markRead.mockResolvedValue({})
  perms.granted = new Set(['team_chat:read', 'team_chat:write'])
  Element.prototype.scrollIntoView = vi.fn()
  document.body.innerHTML = ''
})

describe('reactions', () => {
  it('the picker offers the 12 palette emojis, and picking one reacts and closes it', async () => {
    api.react.mockReturnValue(ok({ reactions: [{ code: 'heart', count: 1, mine: true, names: ['Me'] }] }))
    const { store, wrapper } = render({ messages: [msg('m1', 'ana', 'hello')] })
    await store.fetchChannels()
    await flushPromises()

    await wrapper.get('[data-testid="message-m1"] [aria-label="React"]').trigger('click')
    expect(wrapper.findAll('[data-testid^="pick-m1-"]')).toHaveLength(TEAM_CHAT_REACTIONS.length)
    await wrapper.get('[data-testid="pick-m1-heart"]').trigger('click')
    await flushPromises()

    expect(api.react).toHaveBeenCalledWith('m1', 'heart')
    expect(wrapper.find('[data-testid="picker-m1"]').exists()).toBe(false)
    const chip = wrapper.get('[data-testid="reaction-m1-heart"]')
    expect(chip.text()).toContain('1')
    expect(chip.attributes('aria-pressed')).toBe('true')
  })

  it('clicking a chip I used removes my reaction; one I did not use adds it', async () => {
    api.unreact.mockReturnValue(ok({ reactions: [{ code: 'clap', count: 1, mine: false, names: ['Ana'] }] }))
    api.react.mockReturnValue(ok({ reactions: [{ code: 'clap', count: 2, mine: true, names: ['Ana', 'Me'] }] }))
    const { store, wrapper } = render({ messages: [msg('m1', 'ana', 'hi', { reactions: [
      { code: 'heart', count: 1, mine: true, names: ['Me'] }, { code: 'clap', count: 1, mine: false, names: ['Ana'] }
    ] })] })
    await store.fetchChannels()
    await flushPromises()

    await wrapper.get('[data-testid="reaction-m1-heart"]').trigger('click')
    await flushPromises()
    expect(api.unreact).toHaveBeenCalledWith('m1', 'heart')

    await wrapper.get('[data-testid="reaction-m1-clap"]').trigger('click')
    await flushPromises()
    expect(api.react).toHaveBeenCalledWith('m1', 'clap')
  })

  it('names are in the chip title, and the count past the listed names is shown', async () => {
    const { store, wrapper } = render({ messages: [msg('m1', 'ana', 'hi', { reactions: [
      { code: 'fire', count: 8, mine: false, names: ['A', 'B', 'C', 'D', 'E'] }
    ] })] })
    await store.fetchChannels()
    await flushPromises()
    expect(wrapper.get('[data-testid="reaction-m1-fire"]').attributes('title')).toBe('A, B, C, D, E +3')
  })

  it('read-only users and archived channels see the chips but cannot react, and get no actions', async () => {
    perms.granted = new Set(['team_chat:read'])
    const a = render({ messages: [msg('m1', 'ana', 'hi', { reactions: [{ code: 'heart', count: 1, mine: false, names: ['Ana'] }] })] })
    await a.store.fetchChannels()
    await flushPromises()
    expect(a.wrapper.get('[data-testid="reaction-m1-heart"]').element.tagName).toBe('SPAN')
    expect(a.wrapper.find('[aria-label="React"]').exists()).toBe(false)
    expect(a.wrapper.find('[aria-label="Reply"]').exists()).toBe(false)
    a.wrapper.unmount()

    perms.granted = new Set(['team_chat:read', 'team_chat:write'])
    setActivePinia(createPinia())
    const b = render({ archived: true, messages: [msg('m1', 'ana', 'hi', { reactions: [{ code: 'heart', count: 1, mine: false, names: ['Ana'] }] })] })
    await b.store.fetchChannels()
    await flushPromises()
    expect(b.wrapper.get('[data-testid="reaction-m1-heart"]').element.tagName).toBe('SPAN')
    expect(b.wrapper.find('[aria-label="React"]').exists()).toBe(false)
    expect(b.wrapper.find('[aria-label="Reply"]').exists()).toBe(false)
  })

  it('system messages have no reaction or reply actions', async () => {
    const { store, wrapper } = render({ messages: [{
      id: 's1', channel_id: 'c1', user_id: 'ana', user_name: 'Ana', body: '', kind: 'system',
      meta: { event: 'archived', actor_name: 'Ana' }, created_at: '2026-10-17T12:00:00Z'
    }] })
    await store.fetchChannels()
    await flushPromises()
    expect(wrapper.find('[aria-label="React"]').exists()).toBe(false)
    expect(wrapper.find('[aria-label="Reply"]').exists()).toBe(false)
  })

  it('a refusal is explained in words', async () => {
    api.react.mockImplementation(() => fail(409, 'archived'))
    const { store, wrapper } = render({ messages: [msg('m1', 'ana', 'hi')] })
    await store.fetchChannels()
    await flushPromises()
    await wrapper.get('[aria-label="React"]').trigger('click')
    await wrapper.get('[data-testid="pick-m1-heart"]').trigger('click')
    await flushPromises()
    expect(toast.error).toHaveBeenCalledWith(en.teamChat.errors.archived)
  })

  it('a reactions event refetches that one message and replaces it', async () => {
    const { store, wrapper } = render({ messages: [msg('m1', 'ana', 'hi')] })
    await store.fetchChannels()
    store.activeChannelId = 'c1'
    await flushPromises()
    api.message.mockReturnValue(ok(msg('m1', 'ana', 'hi', { reactions: [{ code: 'party', count: 2, mine: false, names: ['A', 'B'] }] })))
    store.applyEvent('team_chat_message_reactions', { channel_id: 'c1', message_id: 'm1', user_id: 'ana' } as never)
    await flushPromises()
    expect(api.message).toHaveBeenCalledWith('m1')
    expect(wrapper.get('[data-testid="reaction-m1-party"]').text()).toContain('2')
  })
})

describe('replies', () => {
  it('Reply shows a bar with the original, Escape or the X cancels it, and sending sends parent_id and clears it', async () => {
    api.send.mockReturnValue(ok(msg('m2', 'me', 'sure', { parent_id: 'm1' })))
    const { store, wrapper } = render({ messages: [msg('m1', 'ana', 'Can you check order 123?')] })
    await store.fetchChannels()
    await flushPromises()

    await wrapper.get('[data-testid="message-m1"] [aria-label="Reply"]').trigger('click')
    const bar = wrapper.get('[data-testid="reply-bar"]')
    expect(bar.text()).toContain('Replying to Ana')
    expect(bar.text()).toContain('Can you check order 123?')

    await wrapper.get('textarea').trigger('keydown', { key: 'Escape' })
    expect(wrapper.find('[data-testid="reply-bar"]').exists()).toBe(false)

    await wrapper.get('[data-testid="message-m1"] [aria-label="Reply"]').trigger('click')
    await wrapper.get('[aria-label="Cancel reply"]').trigger('click')
    expect(wrapper.find('[data-testid="reply-bar"]').exists()).toBe(false)

    await wrapper.get('[data-testid="message-m1"] [aria-label="Reply"]').trigger('click')
    await wrapper.get('textarea').setValue('sure')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.send).toHaveBeenCalledWith('c1', 'sure', 'm1')
    expect(wrapper.find('[data-testid="reply-bar"]').exists()).toBe(false)
  })

  it('a plain message is sent without parent_id', async () => {
    api.send.mockReturnValue(ok(msg('m2', 'me', 'hello')))
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    await wrapper.get('textarea').setValue('hello')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.send).toHaveBeenCalledWith('c1', 'hello')
  })

  it('shows the quote, and a removed original as "Message removed" without text', async () => {
    const { store, wrapper } = render({ messages: [
      msg('m1', 'ana', 'original'),
      msg('m2', 'me', 'answer', { parent_id: 'm1', reply_to: { id: 'm1', user_id: 'ana', user_name: 'Ana', snippet: 'original', deleted: false } }),
      msg('m3', 'me', 'late', { parent_id: 'gone', reply_to: { id: 'gone', user_name: '', snippet: '', deleted: true } })
    ] })
    await store.fetchChannels()
    await flushPromises()
    expect(wrapper.get('[data-testid="quote-m2"]').text()).toBe('Ana original')
    expect(wrapper.get('[data-testid="quote-m3"]').text()).toBe('Message removed')
  })

  it('clicking the quote scrolls to the original when loaded, and does nothing when it is not', async () => {
    const { store, wrapper } = render({ messages: [
      msg('m1', 'ana', 'original'),
      msg('m2', 'me', 'a', { parent_id: 'm1', reply_to: { id: 'm1', user_name: 'Ana', snippet: 'original', deleted: false } }),
      msg('m3', 'me', 'b', { parent_id: 'old', reply_to: { id: 'old', user_name: 'Ana', snippet: 'older', deleted: false } })
    ] })
    await store.fetchChannels()
    await flushPromises()
    const spy = vi.mocked(Element.prototype.scrollIntoView)
    spy.mockClear()
    await wrapper.get('[data-testid="quote-m3"]').trigger('click')
    expect(spy).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="quote-m2"]').trigger('click')
    expect(spy).toHaveBeenCalledTimes(1)
  })

  it('when the original is gone at send time, the reply bar is dropped, the text kept, and the user told', async () => {
    api.send.mockImplementation(() => fail(400, 'reply_target_gone'))
    const { store, wrapper } = render({ messages: [msg('m1', 'ana', 'original')] })
    await store.fetchChannels()
    await flushPromises()
    await wrapper.get('[aria-label="Reply"]').trigger('click')
    await wrapper.get('textarea').setValue('my answer')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.find('[data-testid="reply-bar"]').exists()).toBe(false)
    expect((wrapper.get('textarea').element as HTMLTextAreaElement).value).toBe('my answer')
    expect(toast.error).toHaveBeenCalledWith(en.teamChat.errors.reply_target_gone)
  })

  it('quote and reply-bar text with HTML stay text', async () => {
    const evil = '<img src=x onerror="window.__xss=1"><b>x</b>'
    const { store, wrapper } = render({ messages: [
      msg('m2', 'me', 'a', { parent_id: 'm1', reply_to: { id: 'm1', user_name: evil, snippet: evil, deleted: false } }),
      msg('m1', 'ana', evil)
    ] })
    await store.fetchChannels()
    await flushPromises()
    expect(wrapper.get('[data-testid="quote-m2"]').find('img, b').exists()).toBe(false)
    await wrapper.get('[data-testid="message-m1"] [aria-label="Reply"]').trigger('click')
    expect(wrapper.get('[data-testid="reply-bar"]').find('img, b').exists()).toBe(false)
    expect((window as unknown as Record<string, unknown>).__xss).toBeUndefined()
  })
})

describe('texts', () => {
  it('every palette code has a label in both languages', () => {
    for (const r of TEAM_CHAT_REACTIONS) {
      expect((en.teamChat.reactions as Record<string, string>)[r.code]).toBeTruthy()
      expect((ptBR.teamChat.reactions as Record<string, string>)[r.code]).toBeTruthy()
    }
  })
})
