// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'

const api = vi.hoisted(() => ({
  channels: vi.fn(), users: vi.fn(), createChannel: vi.fn(), openDM: vi.fn(), messages: vi.fn(),
  send: vi.fn(), edit: vi.fn(), remove: vi.fn(), markRead: vi.fn(), typing: vi.fn()
}))
vi.mock('@/services/api', () => ({ teamChatService: api }))
vi.mock('vue-sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const auth = vi.hoisted(() => ({ granted: new Set<string>() }))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    user: { id: 'me' },
    hasPermission: (r: string, a: string) => auth.granted.has(`${r}:${a}`)
  })
}))

import MessagePanel from './MessagePanel.vue'
import { useTeamChatStore } from '@/stores/teamChat'

const i18n = createI18n({ legacy: false, locale: 'en', messages: { en } })
const ScrollArea = { template: '<div><slot /></div>' }

const msg = (id: string, userId: string, body: string) => ({
  id, channel_id: 'c1', user_id: userId, user_name: userId === 'me' ? 'Me' : 'Ana', body,
  created_at: '2026-10-16T12:00:00Z'
})

function render() {
  const store = useTeamChatStore()
  const wrapper = mount(MessagePanel, {
    props: { channelId: 'c1' },
    global: { plugins: [i18n], stubs: { ScrollArea } }
  })
  return { store, wrapper }
}

beforeEach(() => {
  setActivePinia(createPinia())
  Object.values(api).forEach(f => f.mockReset())
  api.messages.mockResolvedValue({ data: { data: { messages: [], has_more: false } } })
  api.typing.mockResolvedValue({})
  api.markRead.mockResolvedValue({})
  auth.granted = new Set(['team_chat:read', 'team_chat:write'])
  Element.prototype.scrollIntoView = vi.fn()
})

describe('MessagePanel states', () => {
  it('a channel the server refused shows "no access" and offers no way to write', async () => {
    const { store, wrapper } = render()
    store.panel('c1').error = 'forbidden'
    await flushPromises()

    expect(wrapper.text()).toContain(en.teamChat.noAccessTitle)
    expect(wrapper.find('textarea').exists()).toBe(false)
    expect(wrapper.find('[data-testid^="message-"]').exists()).toBe(false)
  })

  it('shows a loading skeleton, then an empty state', async () => {
    const { store, wrapper } = render()
    store.panel('c1').loading = true
    await flushPromises()
    expect(wrapper.find('[data-testid="messages-loading"]').exists()).toBe(true)

    store.panel('c1').loading = false
    await flushPromises()
    expect(wrapper.text()).toContain(en.teamChat.noMessagesTitle)
  })

  it('a failed load offers a retry that asks the API again', async () => {
    const { store, wrapper } = render()
    store.panel('c1').error = 'failed'
    await flushPromises()
    expect(wrapper.text()).toContain(en.teamChat.messagesFailedTitle)

    await wrapper.find('button').trigger('click')
    expect(api.messages).toHaveBeenCalledWith('c1', { limit: 50 })
  })
})

describe('MessagePanel permissions', () => {
  it('without team_chat:write the composer is replaced by a read-only note', async () => {
    auth.granted = new Set(['team_chat:read'])
    const { store, wrapper } = render()
    store.panel('c1').messages = [msg('m1', 'me', 'hi')]
    await flushPromises()

    expect(wrapper.find('textarea').exists()).toBe(false)
    expect(wrapper.text()).toContain(en.teamChat.readOnly)
    expect(wrapper.find('[aria-label="Edit message"]').exists()).toBe(false)
  })

  it('a plain user may edit and delete their own messages only', async () => {
    const { store, wrapper } = render()
    store.panel('c1').messages = [msg('mine', 'me', 'my words'), msg('theirs', 'ana', 'her words')]
    await flushPromises()

    const mine = wrapper.get('[data-testid="message-mine"]')
    const theirs = wrapper.get('[data-testid="message-theirs"]')
    expect(mine.find('[aria-label="Edit message"]').exists()).toBe(true)
    expect(mine.find('[aria-label="Delete message"]').exists()).toBe(true)
    expect(theirs.find('[aria-label="Edit message"]').exists()).toBe(false)
    expect(theirs.find('[aria-label="Delete message"]').exists()).toBe(false)
  })

  it('team_chat:delete adds deleting other people messages, never editing them', async () => {
    auth.granted = new Set(['team_chat:read', 'team_chat:write', 'team_chat:delete'])
    const { store, wrapper } = render()
    store.panel('c1').messages = [msg('theirs', 'ana', 'her words')]
    await flushPromises()

    const theirs = wrapper.get('[data-testid="message-theirs"]')
    expect(theirs.find('[aria-label="Delete message"]').exists()).toBe(true)
    expect(theirs.find('[aria-label="Edit message"]').exists()).toBe(false)
  })
})

describe('MessagePanel content', () => {
  it('renders message text as text, never as HTML', async () => {
    const { store, wrapper } = render()
    const evil = '<img src=x onerror="alert(1)"><b>bold</b>'
    store.panel('c1').messages = [msg('m1', 'ana', evil)]
    await flushPromises()

    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.find('b').exists()).toBe(false)
    expect(wrapper.get('[data-testid="message-m1"]').text()).toContain(evil)
  })

  it('sends with Enter, keeps the line with Shift+Enter, and refuses an empty message', async () => {
    api.send.mockResolvedValue({ data: { data: msg('new', 'me', 'hello') } })
    const { store, wrapper } = render()
    store.panel('c1').messages = []
    await flushPromises()
    const box = wrapper.get('textarea')

    await box.setValue('   ')
    await box.trigger('keydown', { key: 'Enter' })
    expect(api.send).not.toHaveBeenCalled()

    await box.setValue('hello')
    await box.trigger('keydown', { key: 'Enter', shiftKey: true })
    expect(api.send).not.toHaveBeenCalled()

    await box.trigger('keydown', { key: 'Enter' })
    await flushPromises()
    expect(api.send).toHaveBeenCalledWith('c1', 'hello')
    expect(wrapper.get('[data-testid="message-new"]').text()).toContain('hello')
  })

  it('keeps what is typed while a send is still in flight, and sends it next', async () => {
    let finish!: (v: unknown) => void
    api.send.mockReturnValueOnce(new Promise(r => { finish = r }))
    api.send.mockResolvedValueOnce({ data: { data: msg('second', 'me', 'two') } })
    const { wrapper } = render()
    await flushPromises()
    const box = wrapper.get('textarea')

    await box.setValue('one')
    await box.trigger('keydown', { key: 'Enter' })
    expect((box.element as HTMLTextAreaElement).value).toBe('') // cleared at once, not when the server answers

    await box.setValue('two') // typed while the first request is still pending
    await box.trigger('keydown', { key: 'Enter' })
    expect(api.send).toHaveBeenNthCalledWith(2, 'c1', 'two')

    finish({ data: { data: msg('first', 'me', 'one') } })
    await flushPromises()
    expect(wrapper.findAll('[data-testid^="message-"]')).toHaveLength(2)
  })

  it('puts the text back when a send fails, unless something new was typed', async () => {
    api.send.mockRejectedValueOnce({ response: { status: 500 } })
    const { wrapper } = render()
    await flushPromises()
    const box = wrapper.get('textarea')

    await box.setValue('keep me')
    await box.trigger('keydown', { key: 'Enter' })
    await flushPromises()
    expect((box.element as HTMLTextAreaElement).value).toBe('keep me')
  })

  it('announces who is typing', async () => {
    const { store, wrapper } = render()
    store.users = [{ id: 'ana', full_name: 'Ana Souza', is_online: true }]
    store.applyEvent('team_chat_typing', { channel_id: 'c1', user_id: 'ana' })
    await flushPromises()
    expect(wrapper.text()).toContain('Ana Souza is typing')
  })
})
