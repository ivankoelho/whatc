import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

const api = vi.hoisted(() => ({
  channels: vi.fn(), users: vi.fn(), createChannel: vi.fn(), openDM: vi.fn(), messages: vi.fn(),
  send: vi.fn(), edit: vi.fn(), remove: vi.fn(), markRead: vi.fn(), typing: vi.fn(),
  archivedChannels: vi.fn(), members: vi.fn(), addMember: vi.fn(), removeMember: vi.fn(), leave: vi.fn(),
  setRole: vi.fn(), rename: vi.fn(), archive: vi.fn(), reopen: vi.fn(), join: vi.fn(), adminChannels: vi.fn()
}))
vi.mock('@/services/api', () => ({ teamChatService: api }))
const auth = vi.hoisted(() => ({ user: { id: 'me' } as { id: string } | null }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => auth }))

import { useTeamChatStore } from './teamChat'

const ok = (data: unknown) => Promise.resolve({ data: { status: 'success', data } })
const fail = (status: number, code = '') =>
  Promise.reject({ response: { status, data: { error_type: code } } })
const channel = (id: string, extra: Record<string, unknown> = {}) => ({
  id, kind: 'channel', name: id, unread_count: 0, created_at: '2026-10-17T12:00:00Z', ...extra
})
const message = (id: string, at: number, extra: Record<string, unknown> = {}) => ({
  id, channel_id: 'c1', user_id: 'ana', user_name: 'Ana', body: id,
  created_at: new Date(Date.UTC(2026, 9, 17, 12, 0, at)).toISOString(), ...extra
})
const member = (id: string, role = 'member') => ({ user_id: id, name: id, role, is_online: false })

beforeEach(() => {
  setActivePinia(createPinia())
  Object.values(api).forEach(f => f.mockReset())
  api.markRead.mockResolvedValue({})
  api.typing.mockResolvedValue({})
  api.channels.mockReturnValue(ok({ channels: [channel('c1')] }))
  api.archivedChannels.mockReturnValue(ok({ channels: [] }))
  api.messages.mockReturnValue(ok({ messages: [], has_more: false }))
  api.members.mockReturnValue(ok({ members: [member('me', 'owner'), member('ana')] }))
  api.adminChannels.mockReturnValue(ok({ channels: [] }))
  auth.user = { id: 'me' }
})
afterEach(() => vi.useRealTimers())

describe('archived channels', () => {
  it('are loaded on demand and found by id next to the normal ones', async () => {
    const s = useTeamChatStore()
    api.archivedChannels.mockReturnValue(ok({ channels: [channel('old', { archived_at: '2026-10-01T00:00:00Z' })] }))
    await s.fetchChannels()
    expect(s.channelById('old')).toBeNull()

    await s.setShowArchived(true)
    expect(s.showArchived).toBe(true)
    expect(s.archived.map(c => c.id)).toEqual(['old'])
    expect(s.channelById('old')?.archived_at).toBeTruthy()
    expect(s.channelById('c1')?.id).toBe('c1')
    expect(s.channelById(null)).toBeNull()
  })

  it('opening one by link resolves its name and state, still through the API', async () => {
    const s = useTeamChatStore()
    api.archivedChannels.mockReturnValue(ok({ channels: [channel('old', { archived_at: '2026-10-01T00:00:00Z' })] }))
    api.messages.mockReturnValue(ok({ messages: [message('m1', 1)], has_more: false }))
    await s.fetchChannels()
    await s.selectChannel('old')
    expect(api.archivedChannels).toHaveBeenCalled()
    expect(s.activeChannel?.archived_at).toBeTruthy()
    expect(s.activePanel?.messages.map(m => m.id)).toEqual(['m1'])
  })

  it('a link to a channel nobody can see ends in "forbidden", not in someone else\'s data', async () => {
    const s = useTeamChatStore()
    api.messages.mockReturnValue(fail(404, 'not_found'))
    await s.selectChannel('not-mine')
    expect(s.activeChannel).toBeNull()
    expect(s.activePanel?.error).toBe('forbidden')
    expect(s.activePanel?.messages).toEqual([])
  })
})

describe('management actions', () => {
  async function open() {
    const s = useTeamChatStore()
    await s.fetchChannels()
    await s.selectChannel('c1')
    await s.fetchMembers('c1')
    api.channels.mockClear()
    api.messages.mockClear()
    api.members.mockClear()
    return s
  }

  it.each([
    ['addMember', (s: ReturnType<typeof useTeamChatStore>) => s.addMember('c1', 'bia'), 'addMember', ['c1', 'bia']],
    ['removeMember', (s: ReturnType<typeof useTeamChatStore>) => s.removeMember('c1', 'ana'), 'removeMember', ['c1', 'ana']],
    ['setRole', (s: ReturnType<typeof useTeamChatStore>) => s.setRole('c1', 'ana', 'owner'), 'setRole', ['c1', 'ana', 'owner']],
    ['renameChannel', (s: ReturnType<typeof useTeamChatStore>) => s.renameChannel('c1', 'New name'), 'rename', ['c1', 'New name']],
    ['reopenChannel', (s: ReturnType<typeof useTeamChatStore>) => s.reopenChannel('c1'), 'reopen', ['c1']],
    ['joinChannel', (s: ReturnType<typeof useTeamChatStore>) => s.joinChannel('c1', 'a real reason'), 'join', ['c1', 'a real reason']]
  ])('%s calls the API and then re-reads the lists, the conversation and the members', async (_n, run, svc, args) => {
    const s = await open()
    ;(api as Record<string, ReturnType<typeof vi.fn>>)[svc as string].mockResolvedValue({})
    await run(s)
    expect((api as Record<string, ReturnType<typeof vi.fn>>)[svc as string]).toHaveBeenCalledWith(...(args as unknown[]))
    expect(api.channels).toHaveBeenCalled() // the channel list
    expect(api.messages).toHaveBeenCalled() // the latest page: the new system message shows
    expect(api.members).toHaveBeenCalled() // who is in it now
  })

  it('archiving also refreshes the archived list, because the channel just moved there', async () => {
    const s = await open()
    api.archive.mockResolvedValue({})
    api.channels.mockReturnValue(ok({ channels: [] }))
    api.archivedChannels.mockReturnValue(ok({ channels: [channel('c1', { archived_at: '2026-10-17T13:00:00Z' })] }))
    await s.archiveChannel('c1')
    expect(api.archive).toHaveBeenCalledWith('c1')
    expect(s.channels).toEqual([])
    expect(s.channelById('c1')?.archived_at).toBeTruthy()
  })

  it('a refusal reaches the caller and changes nothing', async () => {
    const s = await open()
    api.leave.mockReturnValue(fail(409, 'last_owner'))
    await expect(s.leaveChannel('c1')).rejects.toBeTruthy()
    expect(s.channels.map(c => c.id)).toEqual(['c1'])
    expect(s.activeChannelId).toBe('c1')
    expect(s.members['c1']).toBeTruthy()

    api.addMember.mockReturnValue(fail(403, 'self_add'))
    await expect(s.addMember('c1', 'me')).rejects.toBeTruthy()
    expect(api.messages).not.toHaveBeenCalled() // nothing was re-read, nothing changed
  })

  it('leaving drops the channel, its panel and its members, and closes it', async () => {
    const s = await open()
    api.leave.mockResolvedValue({})
    api.channels.mockReturnValue(ok({ channels: [] }))
    await s.leaveChannel('c1')
    expect(api.leave).toHaveBeenCalledWith('c1')
    expect(s.activeChannelId).toBeNull()
    expect(s.panels['c1']).toBeUndefined()
    expect(s.members['c1']).toBeUndefined()
    expect(s.channels).toEqual([])
  })

  it('members of a channel the user can no longer see are dropped (404)', async () => {
    const s = await open()
    api.members.mockReturnValue(fail(404, 'not_found'))
    await s.fetchMembers('c1')
    expect(s.members['c1']).toBeUndefined()
  })
})

describe('administration list', () => {
  it('loads composition and state, and is kept fresh by later changes', async () => {
    const s = useTeamChatStore()
    api.adminChannels.mockReturnValue(ok({ channels: [{
      id: 'a1', name: 'ops', kind: 'channel', created_at: '2026-10-17T00:00:00Z',
      owners: [member('ana', 'owner')], members: [member('ana', 'owner'), member('bia')]
    }] }))
    await s.fetchAdminChannels()
    expect(s.adminChannels).toHaveLength(1)
    expect(s.adminChannels[0].members).toHaveLength(2)
    expect(JSON.stringify(s.adminChannels)).not.toContain('"body"')
    expect(s.adminError).toBe(false)

    api.addMember.mockResolvedValue({})
    api.adminChannels.mockClear()
    await s.addMember('a1', 'caio')
    expect(api.adminChannels).toHaveBeenCalledTimes(1) // an action in the admin screen refreshes it

    api.adminChannels.mockReturnValue(fail(500))
    await s.fetchAdminChannels()
    expect(s.adminError).toBe(true)
    expect(s.adminChannels).toHaveLength(1) // keeps the last good list
  })
})

describe('team_chat_channel_updated', () => {
  beforeEach(() => vi.useFakeTimers())

  async function open() {
    const s = useTeamChatStore()
    api.channels.mockReturnValue(ok({ channels: [channel('c1'), channel('c2')] }))
    await s.fetchChannels()
    api.messages.mockReturnValueOnce(ok({ messages: [message('m1', 1)], has_more: false }))
    await s.selectChannel('c1')
    await s.fetchMembers('c1')
    api.channels.mockClear(); api.messages.mockClear(); api.members.mockClear(); api.adminChannels.mockClear()
    return s
  }

  it('refreshes the list, and the open channel with its system message and members', async () => {
    const s = await open()
    api.messages.mockReturnValue(ok({ messages: [message('m1', 1), message('sys', 2, { kind: 'system', meta: { event: 'member_added' } })], has_more: false }))

    s.applyEvent('team_chat_channel_updated', { channel_id: 'c1', user_id: 'ana' })
    await vi.advanceTimersByTimeAsync(300)

    expect(api.channels).toHaveBeenCalledTimes(1)
    expect(api.members).toHaveBeenCalledWith('c1')
    expect(s.activePanel?.messages.map(m => m.id)).toEqual(['m1', 'sys'])
    expect(s.activePanel?.messages[1].kind).toBe('system')
  })

  it('for another channel only the lists move; its conversation is not fetched', async () => {
    const s = await open()
    s.applyEvent('team_chat_channel_updated', { channel_id: 'c2', user_id: 'ana' })
    await vi.advanceTimersByTimeAsync(300)
    expect(api.channels).toHaveBeenCalledTimes(1)
    expect(api.messages).not.toHaveBeenCalled()
    expect(api.members).not.toHaveBeenCalled()
  })

  it('a person who was just removed lands in the "no access" state', async () => {
    const s = await open()
    api.channels.mockReturnValue(ok({ channels: [channel('c2')] }))
    api.messages.mockReturnValue(fail(404, 'not_found'))
    api.members.mockReturnValue(fail(404, 'not_found'))

    s.applyEvent('team_chat_channel_updated', { channel_id: 'c1', user_id: 'owner' })
    await vi.advanceTimersByTimeAsync(300)

    expect(s.activePanel?.error).toBe('forbidden')
    expect(s.activePanel?.messages).toEqual([])
    expect(s.members['c1']).toBeUndefined()
    expect(s.channels.map(c => c.id)).toEqual(['c2'])
  })

  it('also refreshes the administration list when that screen is in use, and the archived list when shown', async () => {
    const s = await open()
    await s.fetchAdminChannels()
    await s.setShowArchived(true)
    api.adminChannels.mockClear(); api.archivedChannels.mockClear()

    s.applyEvent('team_chat_channel_updated', { channel_id: 'c2', user_id: 'ana' })
    await vi.advanceTimersByTimeAsync(300)
    expect(api.adminChannels).toHaveBeenCalledTimes(1)
    expect(api.archivedChannels).toHaveBeenCalledTimes(1)
  })
})

describe('reconnect with Phase A data', () => {
  it('re-reads archived channels, the open channel\'s members and the administration list', async () => {
    const s = useTeamChatStore()
    await s.fetchChannels()
    await s.selectChannel('c1')
    await s.fetchMembers('c1')
    await s.setShowArchived(true)
    await s.fetchAdminChannels()
    for (const f of [api.channels, api.archivedChannels, api.messages, api.members, api.adminChannels]) f.mockClear()

    await s.resync()

    for (const f of [api.channels, api.archivedChannels, api.messages, api.members, api.adminChannels]) {
      expect(f).toHaveBeenCalledTimes(1)
    }
  })
})
