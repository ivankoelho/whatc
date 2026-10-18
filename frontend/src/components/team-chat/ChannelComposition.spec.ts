// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'

const api = vi.hoisted(() => ({
  channels: vi.fn(), users: vi.fn(), messages: vi.fn(), members: vi.fn(), archivedChannels: vi.fn(),
  addMember: vi.fn(), removeMember: vi.fn(), leave: vi.fn(), setRole: vi.fn(), rename: vi.fn(),
  archive: vi.fn(), reopen: vi.fn(), join: vi.fn(), adminChannels: vi.fn(), markRead: vi.fn(), typing: vi.fn()
}))
vi.mock('@/services/api', () => ({ teamChatService: api }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 'me' }, hasPermission: () => true }) }))
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock('vue-sonner', () => ({ toast }))

import ChannelComposition from './ChannelComposition.vue'
import type { TeamChatMember } from '@/services/api'

const i18n = createI18n({ legacy: false, locale: 'en', messages: { en } })
const ok = (data: unknown = {}) => Promise.resolve({ data: { data } })
const fail = (status: number, code: string) => Promise.reject({ response: { status, data: { error_type: code } } })

const PickStub = {
  props: ['open', 'title', 'excludeIds'],
  emits: ['update:open', 'pick'],
  template: '<div v-if="open" data-testid="pick-dialog"><button data-testid="pick-bia" @click="$emit(\'pick\', \'bia\')">bia</button></div>'
}
const JoinStub = {
  props: ['open', 'channelId', 'channelName'],
  emits: ['update:open'],
  template: '<div v-if="open" data-testid="join-dialog">{{ channelName }}</div>'
}

const members: TeamChatMember[] = [
  { user_id: 'me', name: 'Me', role: 'owner', is_online: true },
  { user_id: 'ana', name: 'Ana', role: 'member', is_online: false },
  { user_id: 'bruno', name: 'Bruno', role: 'owner', is_online: false }
]

function render(props: Record<string, unknown> = {}) {
  return mount(ChannelComposition, {
    props: {
      channelId: 'c1', name: 'ops', members, canCompose: true, canReopen: false, canJoin: false,
      isMember: true, meId: 'me', ...props
    },
    global: { plugins: [i18n], stubs: { PickPersonDialog: PickStub, AdminJoinDialog: JoinStub } }
  })
}

const has = (w: ReturnType<typeof render>, label: string) => w.find(`[aria-label="${label}"]`).exists()
const buttonText = (w: ReturnType<typeof render>) => w.findAll('button').map(b => b.text()).join('|')

beforeEach(() => {
  setActivePinia(createPinia())
  Object.values(api).forEach(f => f.mockReset())
  api.channels.mockReturnValue(ok({ channels: [] }))
  api.archivedChannels.mockReturnValue(ok({ channels: [] }))
  api.messages.mockReturnValue(ok({ messages: [], has_more: false }))
  api.members.mockReturnValue(ok({ members }))
  api.adminChannels.mockReturnValue(ok({ channels: [] }))
  api.users.mockReturnValue(ok({ users: [] }))
  toast.success.mockReset(); toast.error.mockReset()
  vi.stubGlobal('confirm', vi.fn(() => true))
  window.confirm = vi.fn(() => true)
})

describe('what the buttons offer', () => {
  it('a plain member sees the people and can only leave', () => {
    const w = render({ canCompose: false })
    expect(w.text()).toContain('Ana')
    expect(w.text()).toContain('Bruno')
    expect(buttonText(w)).toContain('Leave channel')
    expect(buttonText(w)).not.toContain('Add a member')
    expect(buttonText(w)).not.toContain('Archive channel')
    expect(has(w, 'Remove Ana')).toBe(false)
    expect(has(w, 'Make Ana an owner')).toBe(false)
    expect(has(w, 'Rename channel')).toBe(false)
  })

  it('the owner (or a manager) can add, remove, change roles, rename and archive', () => {
    const w = render()
    expect(buttonText(w)).toContain('Add a member')
    expect(buttonText(w)).toContain('Archive channel')
    expect(has(w, 'Remove Ana')).toBe(true)
    expect(has(w, 'Make Ana an owner')).toBe(true)
    expect(has(w, 'Make Bruno a regular member')).toBe(true)
    expect(has(w, 'Rename channel')).toBe(true)
  })

  it('never offers actions on oneself in the member list (leaving is its own button)', () => {
    const w = render()
    expect(has(w, 'Remove Me')).toBe(false)
    expect(has(w, 'Make Me a regular member')).toBe(false)
  })

  it('an archived channel is read-only: no adding, renaming, archiving or role changes; removing stays', () => {
    const w = render({ archivedAt: '2026-10-01T00:00:00Z' })
    expect(w.text()).toContain('Archived')
    expect(buttonText(w)).not.toContain('Add a member')
    expect(buttonText(w)).not.toContain('Archive channel')
    expect(has(w, 'Rename channel')).toBe(false)
    expect(has(w, 'Make Ana an owner')).toBe(false)
    expect(has(w, 'Remove Ana')).toBe(true)
  })

  it('only team_chat:manage gets "Reopen", and only while archived', () => {
    expect(buttonText(render({ archivedAt: '2026-10-01T00:00:00Z', canReopen: false }))).not.toContain('Reopen channel')
    expect(buttonText(render({ archivedAt: '2026-10-01T00:00:00Z', canReopen: true }))).toContain('Reopen channel')
    expect(buttonText(render({ canReopen: true }))).not.toContain('Reopen channel')
  })

  it('"Enter as administrator" shows only with join_any and for a non-member', async () => {
    expect(buttonText(render({ canJoin: false }))).not.toContain('Enter as administrator')
    const w = render({ canJoin: true, isMember: false, canCompose: true })
    expect(buttonText(w)).toContain('Enter as administrator')
    expect(buttonText(w)).not.toContain('Leave channel')
    await w.findAll('button').find(b => b.text().includes('Enter as administrator'))!.trigger('click')
    expect(w.get('[data-testid="join-dialog"]').text()).toBe('ops')
  })
})

describe('actions', () => {
  it('adding offers only people who are not in the channel yet, then calls the API', async () => {
    const w = render()
    await w.findAll('button').find(b => b.text().includes('Add a member'))!.trigger('click')
    const dialog = w.findComponent(PickStub)
    expect(dialog.props('excludeIds')).toEqual(['me', 'ana', 'bruno'])
    await w.get('[data-testid="pick-bia"]').trigger('click')
    await flushPromises()
    expect(api.addMember).toHaveBeenCalledWith('c1', 'bia')
  })

  it('removing asks first and does nothing when declined', async () => {
    const w = render()
    window.confirm = vi.fn(() => false)
    await w.get('[aria-label="Remove Ana"]').trigger('click')
    await flushPromises()
    expect(api.removeMember).not.toHaveBeenCalled()

    window.confirm = vi.fn(() => true)
    api.removeMember.mockReturnValue(ok())
    await w.get('[aria-label="Remove Ana"]').trigger('click')
    await flushPromises()
    expect(api.removeMember).toHaveBeenCalledWith('c1', 'ana')
  })

  it('promoting and demoting toggle the role', async () => {
    api.setRole.mockReturnValue(ok())
    const w = render()
    await w.get('[aria-label="Make Ana an owner"]').trigger('click')
    await flushPromises() // the screen ignores a second click while an action is still running
    await w.get('[aria-label="Make Bruno a regular member"]').trigger('click')
    await flushPromises()
    expect(api.setRole).toHaveBeenNthCalledWith(1, 'c1', 'ana', 'owner')
    expect(api.setRole).toHaveBeenNthCalledWith(2, 'c1', 'bruno', 'member')
  })

  it('leaving emits "left"; the last owner is told why they cannot, and nothing is emitted', async () => {
    api.leave.mockReturnValueOnce(fail(409, 'last_owner'))
    const w = render()
    const leave = () => w.findAll('button').find(b => b.text().includes('Leave channel'))!.trigger('click')
    await leave()
    await flushPromises()
    expect(toast.error).toHaveBeenCalledWith(en.teamChat.errors.last_owner)
    expect(w.emitted('left')).toBeUndefined()

    api.leave.mockReturnValueOnce(ok())
    await leave()
    await flushPromises()
    expect(w.emitted('left')).toHaveLength(1)
  })

  it('every server refusal is explained in words, not shown raw', async () => {
    const cases: Array<[string, string]> = [
      ['forbidden', en.teamChat.errors.forbidden],
      ['archived', en.teamChat.errors.archived],
      ['already_member', en.teamChat.errors.already_member],
      ['something_new', en.teamChat.errors.generic]
    ]
    for (const [code, text] of cases) {
      toast.error.mockReset()
      api.removeMember.mockReturnValueOnce(fail(403, code))
      const w = render()
      await w.get('[aria-label="Remove Ana"]').trigger('click')
      await flushPromises()
      expect(toast.error).toHaveBeenCalledWith(text)
    }
  })

  it('renaming trims the name, and an unchanged or blank name sends nothing', async () => {
    api.rename.mockReturnValue(ok())
    const w = render()
    const openRename = () => w.get('[aria-label="Rename channel"]').trigger('click')

    await openRename()
    await w.get('input').setValue('   ')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(api.rename).not.toHaveBeenCalled()

    await openRename()
    await w.get('input').setValue('ops')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(api.rename).not.toHaveBeenCalled()

    await openRename()
    await w.get('input').setValue('  logistics  ')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(api.rename).toHaveBeenCalledWith('c1', 'logistics')
  })

  it('archiving asks first', async () => {
    api.archive.mockReturnValue(ok())
    const w = render()
    window.confirm = vi.fn(() => false)
    await w.findAll('button').find(b => b.text().includes('Archive channel'))!.trigger('click')
    await flushPromises()
    expect(api.archive).not.toHaveBeenCalled()
    window.confirm = vi.fn(() => true)
    await w.findAll('button').find(b => b.text().includes('Archive channel'))!.trigger('click')
    await flushPromises()
    expect(api.archive).toHaveBeenCalledWith('c1')
  })
})
