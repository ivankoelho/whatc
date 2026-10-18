// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'
import ptBR from '@/i18n/locales/pt-BR.json'

const api = vi.hoisted(() => ({
  channels: vi.fn(), archivedChannels: vi.fn(), users: vi.fn(), messages: vi.fn(), members: vi.fn(),
  adminChannels: vi.fn(), markRead: vi.fn(), typing: vi.fn(), send: vi.fn(), react: vi.fn(), unreact: vi.fn(), message: vi.fn(),
  uploadAttachment: vi.fn(), discardAttachment: vi.fn()
}))
vi.mock('@/services/api', () => ({ teamChatService: api }))
vi.mock('vue-sonner', () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
const perms = vi.hoisted(() => ({ granted: new Set<string>() }))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ user: { id: 'me' }, hasPermission: (r: string, a: string) => perms.granted.has(`${r}:${a}`) })
}))

import MessagePanel from './MessagePanel.vue'
import { useTeamChatStore } from '@/stores/teamChat'
import { EMOJI_CATEGORIES } from '@/lib/team-chat-emoji'

const ScrollArea = { template: '<div class="scroll-stub"><slot /></div>' }
const ManageStub = { props: ['open', 'channelId'], template: '<div />' }
const ok = (data: unknown = {}) => Promise.resolve({ data: { data } })
const msg = (id: string, body: string) => ({
  id, channel_id: 'c1', user_id: 'ana', user_name: 'Ana', body, kind: 'user',
  created_at: '2026-10-17T12:00:00Z', reactions: [], attachments: []
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

const pointerDownOutside = () => document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }))
const escape = () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))

beforeEach(() => {
  setActivePinia(createPinia())
  Object.values(api).forEach(f => f.mockReset())
  api.users.mockReturnValue(ok({ users: [] }))
  api.messages.mockReturnValue(ok({ messages: [], has_more: false }))
  api.members.mockReturnValue(ok({ members: [] }))
  api.typing.mockResolvedValue({})
  api.markRead.mockResolvedValue({})
  perms.granted = new Set(['team_chat:read', 'team_chat:write'])
  Element.prototype.scrollIntoView = vi.fn()
  document.body.innerHTML = ''
})

describe('emoji while writing', () => {
  it('inserts at the caret, keeps the text around it and returns focus to the box', async () => {
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    const box = wrapper.get('textarea')
    await box.setValue('olá  mundo')
    const el = box.element as HTMLTextAreaElement
    el.setSelectionRange(4, 4)

    await wrapper.get('[data-testid="emoji-button"]').trigger('click')
    expect(wrapper.find('[data-testid="emoji-picker"]').exists()).toBe(true)
    await wrapper.get('[data-testid="emoji-😀"]').trigger('click')
    await flushPromises()
    expect(el.value).toBe('olá 😀 mundo')
    expect(el.selectionStart).toBe(4 + '😀'.length)
    expect(document.activeElement).toBe(el)
  })

  it('replaces a selection, appends when the box is empty, and can insert several in a row', async () => {
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    const el = wrapper.get('textarea').element as HTMLTextAreaElement
    await wrapper.get('[data-testid="emoji-button"]').trigger('click')
    await wrapper.get('[data-testid="emoji-😀"]').trigger('click')
    await wrapper.get('[data-testid="emoji-😂"]').trigger('click')
    await flushPromises()
    expect(el.value).toBe('😀😂')
    el.setSelectionRange(0, '😀😂'.length)
    await wrapper.get('[data-testid="emoji-🤔"]').trigger('click')
    await flushPromises()
    expect(el.value).toBe('🤔')
  })

  it('every category has its tab, in both languages, and switching shows that category', async () => {
    for (const locale of ['en', 'pt-BR'] as const) {
      setActivePinia(createPinia())
      document.body.innerHTML = ''
      const { store, wrapper } = render({ locale })
      await store.fetchChannels()
      await flushPromises()
      await wrapper.get('[data-testid="emoji-button"]').trigger('click')
      for (const c of EMOJI_CATEGORIES) {
        const label = (locale === 'en' ? en : ptBR).teamChat.emoji.categories[c.id as 'smileys']
        expect(label).toBeTruthy()
        const tab = wrapper.get(`[data-testid="emoji-tab-${c.id}"]`)
        expect(tab.text()).toBe(label)
        await tab.trigger('click')
        const shown = wrapper.get('[data-testid="emoji-picker"]').findAll('button:not([role="tab"])')
        expect(shown.length).toBe(c.items.length)
      }
      wrapper.unmount()
    }
  })

  it('closes on Escape, on a click elsewhere, and on switching conversation; a click inside does not close it', async () => {
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    const open = async () => {
      if (!wrapper.find('[data-testid="emoji-picker"]').exists()) await wrapper.get('[data-testid="emoji-button"]').trigger('click')
    }
    await open()
    await wrapper.get('[data-testid="emoji-tab-people"]').trigger('click')
    expect(wrapper.find('[data-testid="emoji-picker"]').exists()).toBe(true)
    escape()
    await flushPromises()
    expect(wrapper.find('[data-testid="emoji-picker"]').exists()).toBe(false)

    await open()
    pointerDownOutside()
    await flushPromises()
    expect(wrapper.find('[data-testid="emoji-picker"]').exists()).toBe(false)

    await open()
    await wrapper.setProps({ channelId: 'c2' })
    expect(wrapper.find('[data-testid="emoji-picker"]').exists()).toBe(false)
  })

  it('Escape inside the box closes the picker first, and only then cancels a reply', async () => {
    const { store, wrapper } = render({ messages: [msg('m1', 'oi')] })
    await store.fetchChannels()
    await flushPromises()
    await wrapper.get('[aria-label="Reply"]').trigger('click')
    await wrapper.get('[data-testid="emoji-button"]').trigger('click')
    await wrapper.get('textarea').trigger('keydown', { key: 'Escape' })
    expect(wrapper.find('[data-testid="emoji-picker"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="reply-bar"]').exists()).toBe(true)
    await wrapper.get('textarea').trigger('keydown', { key: 'Escape' })
    expect(wrapper.find('[data-testid="reply-bar"]').exists()).toBe(false)
  })

  it('is not offered to read-only users or in archived channels', async () => {
    perms.granted = new Set(['team_chat:read'])
    const a = render()
    await a.store.fetchChannels()
    await flushPromises()
    expect(a.wrapper.find('[data-testid="emoji-button"]').exists()).toBe(false)
    a.wrapper.unmount()
    perms.granted = new Set(['team_chat:read', 'team_chat:write'])
    setActivePinia(createPinia())
    const b = render({ archived: true })
    await b.store.fetchChannels()
    await flushPromises()
    expect(b.wrapper.find('[data-testid="emoji-button"]').exists()).toBe(false)
  })

  it('what is typed with an emoji is sent as written', async () => {
    api.send.mockReturnValue(ok({ ...msg('m9', 'bom dia 😀'), user_id: 'me' }))
    const { store, wrapper } = render()
    await store.fetchChannels()
    await flushPromises()
    await wrapper.get('textarea').setValue('bom dia ')
    ;(wrapper.get('textarea').element as HTMLTextAreaElement).setSelectionRange(8, 8)
    await wrapper.get('[data-testid="emoji-button"]').trigger('click')
    await wrapper.get('[data-testid="emoji-😀"]').trigger('click')
    await flushPromises()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.send).toHaveBeenCalledWith('c1', 'bom dia 😀')
  })
})

describe('reaction picker', () => {
  it('has a fixed size (not squeezed to the width of the buttons) and closes on Escape or an outside click', async () => {
    const { store, wrapper } = render({ messages: [msg('m1', 'oi')] })
    await store.fetchChannels()
    await flushPromises()
    await wrapper.get('[data-testid="message-m1"] [aria-label="React"]').trigger('click')
    const picker = wrapper.get('[data-testid="picker-m1"]')
    expect(picker.classes()).toContain('w-max')
    expect(picker.classes()).toContain('grid-cols-6')
    escape()
    await flushPromises()
    expect(wrapper.find('[data-testid="picker-m1"]').exists()).toBe(false)

    await wrapper.get('[data-testid="message-m1"] [aria-label="React"]').trigger('click')
    pointerDownOutside()
    await flushPromises()
    expect(wrapper.find('[data-testid="picker-m1"]').exists()).toBe(false)
  })
})

describe('long messages wrap inside the pane', () => {
  it('the list undoes the viewport wrapper\'s min-width: fit-content, and text may break anywhere', async () => {
    const long = 'A'.repeat(500)
    const { store, wrapper } = render({ messages: [msg('m1', long)] })
    await store.fetchChannels()
    await flushPromises()
    // The ScrollArea gets the override that lets a long line shrink to the pane instead of stretching it.
    expect(wrapper.get('.scroll-stub').classes()).toContain('[&_[data-reka-scroll-area-viewport]>div]:!min-w-0')
    const p = wrapper.get('[data-testid="message-m1"] p')
    expect(p.classes()).toContain('whitespace-pre-wrap')
    expect(p.classes().join(' ')).toContain('[overflow-wrap:anywhere]')
    expect(p.text()).toBe(long) // nothing is cut or abbreviated
    const column = p.element.parentElement as HTMLElement
    expect(column.className).toContain('max-w-3xl') // a readable measure with a margin, not the full width of a wide screen
    expect(column.className).toContain('min-w-0')
  })
})
