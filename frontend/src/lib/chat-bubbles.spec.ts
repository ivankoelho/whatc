import { describe, expect, it } from 'vitest'
import { bubbleMeta, dayLabel, isOwnMessage } from './chat-bubbles'

const m = (id: string, user: string, at: string, kind = 'user') => ({ id, user_id: user, created_at: at, kind })

describe('chat bubbles', () => {
  it('the side is decided by the author id and nothing else', () => {
    expect(isOwnMessage({ user_id: 'u1' }, 'u1')).toBe(true)
    expect(isOwnMessage({ user_id: 'u2' }, 'u1')).toBe(false)
    expect(isOwnMessage({ user_id: 'u1' }, undefined)).toBe(false)
  })

  it('groups consecutive messages of the same sender and splits on sender, gap, system message and day', () => {
    const list = [
      m('1', 'a', '2026-10-26T10:00:00'),
      m('2', 'a', '2026-10-26T10:02:00'), // same run
      m('3', 'b', '2026-10-26T10:03:00'), // other sender
      m('4', 'b', '2026-10-26T10:20:00'), // long pause
      m('5', 'sys', '2026-10-26T10:21:00', 'system'),
      m('6', 'b', '2026-10-26T10:22:00'), // after a system message
      m('7', 'b', '2026-10-27T09:00:00'), // next day
    ]
    expect(list.map((_, i) => bubbleMeta(list, i).first)).toEqual([true, false, true, true, true, true, true])
    expect(list.map((_, i) => bubbleMeta(list, i).newDay)).toEqual([true, false, false, false, false, false, true])
  })

  it('labels today, yesterday and older days', () => {
    const now = new Date('2026-10-26T12:00:00')
    const labels = { today: 'Hoje', yesterday: 'Ontem' }
    expect(dayLabel('2026-10-26T08:00:00', 'pt-BR', labels, now)).toBe('Hoje')
    expect(dayLabel('2026-10-25T23:00:00', 'pt-BR', labels, now)).toBe('Ontem')
    expect(dayLabel('2026-10-20T08:00:00', 'pt-BR', labels, now)).toMatch(/20/)
  })
})
