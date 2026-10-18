import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

const api = vi.hoisted(() => ({
  channels: vi.fn(),
  users: vi.fn(),
  createChannel: vi.fn(),
  openDM: vi.fn(),
  messages: vi.fn(),
  send: vi.fn(),
  edit: vi.fn(),
  remove: vi.fn(),
  markRead: vi.fn(),
  typing: vi.fn()
}))
vi.mock('@/services/api', () => ({ teamChatService: api }))

const auth = vi.hoisted(() => ({ user: { id: 'me' } as { id: string } | null }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => auth }))

import { useTeamChatStore } from './teamChat'

const ok = (data: unknown) => Promise.resolve({ data: { status: 'success', data } })
const notFound = () => Promise.reject({ response: { status: 404 } })
const channel = (id: string, unread = 0) => ({
  id, kind: 'channel', name: id, unread_count: unread, created_at: '2026-10-16T12:00:00Z'
})
const message = (id: string, at: number, channelId = 'c1', userId = 'ana') => ({
  id, channel_id: channelId, user_id: userId, user_name: userId, body: id,
  created_at: new Date(Date.UTC(2026, 9, 16, 12, 0, at)).toISOString()
})

beforeEach(() => {
  setActivePinia(createPinia())
  Object.values(api).forEach(f => f.mockReset())
  api.markRead.mockResolvedValue({})
  api.typing.mockResolvedValue({})
  auth.user = { id: 'me' }
})
afterEach(() => vi.useRealTimers())

describe('channels', () => {
  it('lists what the API returns and flags a failure', async () => {
    const s = useTeamChatStore()
    api.channels.mockReturnValueOnce(ok({ channels: [channel('c1', 2), channel('c2', 3)] }))
    await s.fetchChannels()
    expect(s.channels.map(c => c.id)).toEqual(['c1', 'c2'])
    expect(s.totalUnread).toBe(5)
    expect(s.channelsError).toBe(false)

    api.channels.mockRejectedValueOnce(new Error('offline'))
    await s.fetchChannels()
    expect(s.channelsError).toBe(true)
    expect(s.channels).toHaveLength(2) // keeps the last good list
  })
})

describe('opening a channel', () => {
  it('loads the messages, marks the channel read and clears its badge', async () => {
    const s = useTeamChatStore()
    api.channels.mockReturnValueOnce(ok({ channels: [channel('c1', 4)] }))
    await s.fetchChannels()
    api.messages.mockReturnValueOnce(ok({ messages: [message('m1', 1), message('m2', 2)], has_more: true }))

    await s.selectChannel('c1')

    expect(api.messages).toHaveBeenCalledWith('c1', { limit: 50 })
    expect(s.activePanel?.messages.map(m => m.id)).toEqual(['m1', 'm2'])
    expect(s.activePanel?.hasMore).toBe(true)
    expect(api.markRead).toHaveBeenCalledWith('c1')
    expect(s.channels[0].unread_count).toBe(0)
  })

  it('a channel the server refuses (404) ends in "forbidden", with no messages and no read marker', async () => {
    const s = useTeamChatStore()
    api.messages.mockReturnValueOnce(notFound())

    await s.selectChannel('someone-elses-channel')

    expect(s.activePanel?.error).toBe('forbidden')
    expect(s.activePanel?.messages).toEqual([])
    expect(api.markRead).not.toHaveBeenCalled()
  })

  it('any other failure is a retryable error, not "forbidden"', async () => {
    const s = useTeamChatStore()
    api.messages.mockRejectedValueOnce({ response: { status: 500 } })
    await s.selectChannel('c1')
    expect(s.activePanel?.error).toBe('failed')
    expect(api.markRead).not.toHaveBeenCalled()
  })

  it('pages older messages with the cursor of the oldest one held, one request at a time', async () => {
    const s = useTeamChatStore()
    api.messages.mockReturnValueOnce(ok({ messages: [message('m3', 3), message('m4', 4)], has_more: true }))
    await s.selectChannel('c1')

    let release!: (v: unknown) => void
    api.messages.mockReturnValueOnce(new Promise(r => { release = r }))
    const first = s.loadOlder('c1')
    const second = s.loadOlder('c1') // ignored: one already in flight
    release({ data: { data: { messages: [message('m1', 1), message('m2', 2)], has_more: false } } })
    await Promise.all([first, second])

    expect(api.messages).toHaveBeenCalledTimes(2)
    expect(api.messages).toHaveBeenLastCalledWith('c1', { limit: 50, before_id: 'm3' })
    expect(s.activePanel?.messages.map(m => m.id)).toEqual(['m1', 'm2', 'm3', 'm4'])
    expect(s.activePanel?.hasMore).toBe(false)
    await s.loadOlder('c1') // nothing more: no request
    expect(api.messages).toHaveBeenCalledTimes(2)
  })
})

describe('sending', () => {
  it('shows the message once even when the event for it arrives too', async () => {
    const s = useTeamChatStore()
    api.messages.mockReturnValueOnce(ok({ messages: [], has_more: false }))
    await s.selectChannel('c1')
    api.send.mockReturnValueOnce(ok(message('m1', 1, 'c1', 'me')))
    await s.send('c1', 'hi')

    s.applyEvent('team_chat_message', { channel_id: 'c1', message_id: 'm1', user_id: 'me' })

    expect(api.messages).toHaveBeenCalledTimes(1) // the event for a message we already hold costs nothing
    expect(s.activePanel?.messages.map(m => m.id)).toEqual(['m1'])
  })
})

describe('events (ids only, content always from REST)', () => {
  beforeEach(() => vi.useFakeTimers())

  async function open() {
    const s = useTeamChatStore()
    api.channels.mockReturnValue(ok({ channels: [channel('c1', 0), channel('c2', 0)] }))
    await s.fetchChannels()
    api.messages.mockReturnValueOnce(ok({ messages: [message('m1', 1)], has_more: false }))
    await s.selectChannel('c1')
    api.messages.mockClear()
    api.markRead.mockClear()
    api.channels.mockClear()
    return s
  }

  it('a new message in the open channel re-reads the page and marks it read', async () => {
    const s = await open()
    api.messages.mockReturnValueOnce(ok({ messages: [message('m1', 1), message('m2', 2)], has_more: false }))

    s.applyEvent('team_chat_message', { channel_id: 'c1', message_id: 'm2', user_id: 'ana' })
    await vi.advanceTimersByTimeAsync(0)

    expect(api.messages).toHaveBeenCalledTimes(1)
    expect(s.activePanel?.messages.map(m => m.id)).toEqual(['m1', 'm2'])
    expect(api.markRead).toHaveBeenCalledWith('c1')
  })

  it('a message in another channel only reloads the channel list, once for a burst', async () => {
    const s = await open()
    api.channels.mockReturnValue(ok({ channels: [channel('c1', 0), channel('c2', 3)] }))

    for (let i = 0; i < 5; i++) s.applyEvent('team_chat_message', { channel_id: 'c2', message_id: `x${i}`, user_id: 'ana' })
    await vi.advanceTimersByTimeAsync(300)

    expect(api.messages).not.toHaveBeenCalled()
    expect(api.channels).toHaveBeenCalledTimes(1)
    expect(s.channels.find(c => c.id === 'c2')?.unread_count).toBe(3)
  })

  it('edits and deletions refresh the open channel and drop the cache of closed ones', async () => {
    const s = await open()
    api.messages.mockReturnValueOnce(ok({ messages: [], has_more: false }))
    s.applyEvent('team_chat_message_deleted', { channel_id: 'c1', message_id: 'm1', user_id: 'ana' })
    await vi.advanceTimersByTimeAsync(0)
    expect(s.activePanel?.messages).toEqual([])

    s.panel('c2').messages = [message('z', 1, 'c2') as never]
    s.applyEvent('team_chat_message_updated', { channel_id: 'c2', message_id: 'z', user_id: 'ana' })
    expect(s.panels['c2']).toBeUndefined()
    expect(api.messages).toHaveBeenCalledTimes(1)
  })

  it('losing access mid-conversation turns into "forbidden" on the next refresh', async () => {
    const s = await open()
    api.messages.mockReturnValueOnce(notFound())
    s.applyEvent('team_chat_message', { channel_id: 'c1', message_id: 'm9', user_id: 'ana' })
    await vi.advanceTimersByTimeAsync(0)
    expect(s.activePanel?.error).toBe('forbidden')
    expect(s.activePanel?.messages).toEqual([])
  })

  it('a read event from another tab clears the badge without a request', async () => {
    const s = await open()
    s.channels[1].unread_count = 7
    s.applyEvent('team_chat_read', { channel_id: 'c2', user_id: 'me' })
    expect(s.channels[1].unread_count).toBe(0)
    expect(api.markRead).not.toHaveBeenCalled()
  })

  it('a new channel event reloads the list', async () => {
    const s = await open()
    api.channels.mockReturnValue(ok({ channels: [channel('c1'), channel('c2'), channel('c3')] }))
    s.applyEvent('team_chat_channel', { channel_id: 'c3', user_id: 'ana' })
    await vi.advanceTimersByTimeAsync(300)
    expect(s.channels.map(c => c.id)).toEqual(['c1', 'c2', 'c3'])
  })

  it('shows who is typing, ignores yourself, and expires after a few seconds', async () => {
    const s = await open()
    s.applyEvent('team_chat_typing', { channel_id: 'c1', user_id: 'ana' })
    s.applyEvent('team_chat_typing', { channel_id: 'c1', user_id: 'me' })
    expect(s.typingUserIds('c1')).toEqual(['ana'])

    await vi.advanceTimersByTimeAsync(5001)
    expect(s.typingUserIds('c1')).toEqual([])
  })

  it('typing renewed before expiry stays visible', async () => {
    const s = await open()
    s.applyEvent('team_chat_typing', { channel_id: 'c1', user_id: 'ana' })
    await vi.advanceTimersByTimeAsync(3000)
    s.applyEvent('team_chat_typing', { channel_id: 'c1', user_id: 'ana' })
    await vi.advanceTimersByTimeAsync(3000) // 6s after the first, 3s after the second
    expect(s.typingUserIds('c1')).toEqual(['ana'])
    await vi.advanceTimersByTimeAsync(2500)
    expect(s.typingUserIds('c1')).toEqual([])
  })

  it('presence updates the people directory', async () => {
    const s = useTeamChatStore()
    api.users.mockReturnValueOnce(ok({ users: [{ id: 'ana', full_name: 'Ana', is_online: false }] }))
    await s.fetchUsers()
    s.applyPresence('ana', true)
    expect(s.users[0].is_online).toBe(true)
    expect(s.userNames).toEqual({ ana: 'Ana' })
  })
})

describe('reconnect', () => {
  it('does nothing for someone who never opened the chat', async () => {
    const s = useTeamChatStore()
    await s.resync()
    expect(api.channels).not.toHaveBeenCalled()
    expect(api.messages).not.toHaveBeenCalled()
  })

  it('re-reads the channels and the open conversation', async () => {
    const s = useTeamChatStore()
    api.channels.mockReturnValue(ok({ channels: [channel('c1', 0)] }))
    api.messages.mockReturnValueOnce(ok({ messages: [message('m1', 1)], has_more: false }))
    await s.fetchChannels()
    await s.selectChannel('c1')
    api.channels.mockClear()
    api.channels.mockReturnValue(ok({ channels: [channel('c1', 2)] }))
    api.messages.mockReturnValueOnce(ok({ messages: [message('m1', 1), message('m2', 2)], has_more: false }))

    await s.resync()

    expect(s.channels[0].unread_count).toBe(2)
    expect(s.activePanel?.messages.map(m => m.id)).toEqual(['m1', 'm2'])
  })
})

describe('typing notifications', () => {
  it('are throttled per channel', () => {
    vi.useFakeTimers()
    const s = useTeamChatStore()
    s.notifyTyping('c1')
    s.notifyTyping('c1')
    s.notifyTyping('c2')
    expect(api.typing).toHaveBeenCalledTimes(2)
    vi.advanceTimersByTime(3100)
    s.notifyTyping('c1')
    expect(api.typing).toHaveBeenCalledTimes(3)
  })
})
