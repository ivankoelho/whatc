import { describe, expect, it } from 'vitest'
import type { TeamChatMessage } from '@/services/api'
import { appendUnique, isSendableBody, mergeLatest, pickTypingNames, TEAM_CHAT_MAX_BODY } from './team-chat'

const msg = (id: string, at: string, body = id): TeamChatMessage => ({
  id, channel_id: 'c1', user_id: 'u1', user_name: 'Ana', body, created_at: at
})
const t = (s: number) => new Date(Date.UTC(2026, 9, 16, 12, 0, s)).toISOString()
const ids = (l: TeamChatMessage[]) => l.map(m => m.id)

describe('mergeLatest', () => {
  it('uses the page as the whole history when it reaches the start of the channel', () => {
    const held = [msg('a', t(1)), msg('b', t(2)), msg('c', t(3))]
    // b was deleted, c edited, d is new
    const latest = [msg('a', t(1)), msg('c', t(3), 'edited'), msg('d', t(4))]
    const out = mergeLatest(held, latest, false)
    expect(ids(out)).toEqual(['a', 'c', 'd'])
    expect(out[1].body).toBe('edited')
  })

  it('keeps older messages already loaded by scrolling up', () => {
    const held = [msg('a', t(1)), msg('b', t(2)), msg('c', t(3)), msg('d', t(4))]
    const latest = [msg('c', t(3)), msg('d', t(4)), msg('e', t(5))]
    expect(ids(mergeLatest(held, latest, true))).toEqual(['a', 'b', 'c', 'd', 'e'])
  })

  it('shows an edit and a deletion that happened inside the latest page', () => {
    const held = [msg('a', t(1)), msg('b', t(2)), msg('c', t(3))]
    const latest = [msg('b', t(2), 'edited'), msg('c', t(3))] // a still exists server side (older), nothing deleted here
    const out = mergeLatest(held, latest, true)
    expect(ids(out)).toEqual(['a', 'b', 'c'])
    expect(out[1].body).toBe('edited')

    const afterDelete = mergeLatest(out, [msg('b', t(2), 'edited')], true) // c deleted
    expect(ids(afterDelete)).toEqual(['a', 'b'])
  })

  it('falls back to timestamps when the page oldest message is not held yet', () => {
    const held = [msg('a', t(1)), msg('b', t(2))]
    const latest = [msg('x', t(10)), msg('y', t(11))]
    expect(ids(mergeLatest(held, latest, true))).toEqual(['a', 'b', 'x', 'y'])
  })

  it('returns an empty list for an empty channel', () => {
    expect(mergeLatest([msg('a', t(1))], [], true)).toEqual([])
    expect(mergeLatest([], [], false)).toEqual([])
  })

  it('does not mutate its inputs', () => {
    const held = [msg('a', t(1))]
    const latest = [msg('a', t(1)), msg('b', t(2))]
    mergeLatest(held, latest, false)
    expect(ids(held)).toEqual(['a'])
    expect(ids(latest)).toEqual(['a', 'b'])
  })
})

describe('appendUnique', () => {
  it('adds a new message once, however many times it arrives', () => {
    const base = [msg('a', t(1))]
    const once = appendUnique(base, msg('b', t(2)))
    const twice = appendUnique(once, msg('b', t(2)))
    expect(ids(twice)).toEqual(['a', 'b'])
  })
})

describe('isSendableBody', () => {
  it('rejects empty and whitespace-only text', () => {
    expect(isSendableBody('')).toBe(false)
    expect(isSendableBody('  \n ')).toBe(false)
  })
  it('accepts up to the limit and rejects above it, counting characters not bytes', () => {
    expect(isSendableBody('x'.repeat(TEAM_CHAT_MAX_BODY))).toBe(true)
    expect(isSendableBody('x'.repeat(TEAM_CHAT_MAX_BODY + 1))).toBe(false)
    expect(isSendableBody('😀'.repeat(TEAM_CHAT_MAX_BODY))).toBe(true) // 4000 characters, 8000 UTF-16 units
  })
})

describe('pickTypingNames', () => {
  it('names the people it knows and skips the rest', () => {
    expect(pickTypingNames(['1', '2', '3'], { '1': 'Ana', '3': 'Caio' })).toEqual(['Ana', 'Caio'])
  })
})

describe('chatErrorCode', () => {
  it('reads the server reason, and is empty for anything else', async () => {
    const { chatErrorCode } = await import('./team-chat')
    expect(chatErrorCode({ response: { data: { error_type: 'last_owner' } } })).toBe('last_owner')
    expect(chatErrorCode({ response: { data: {} } })).toBe('')
    expect(chatErrorCode(new Error('offline'))).toBe('')
    expect(chatErrorCode(null)).toBe('')
  })
})

describe('systemEventKey', () => {
  it('maps the eight known events to their text, and nothing else', async () => {
    const { systemEventKey, TEAM_CHAT_SYSTEM_EVENTS } = await import('./team-chat')
    for (const ev of TEAM_CHAT_SYSTEM_EVENTS) expect(systemEventKey({ event: ev })).toBe(`teamChat.system.${ev}`)
    expect(TEAM_CHAT_SYSTEM_EVENTS).toHaveLength(8)
    expect(systemEventKey({ event: 'drop_table' })).toBe('')
    expect(systemEventKey({})).toBe('')
    expect(systemEventKey(undefined)).toBe('')
  })
})
