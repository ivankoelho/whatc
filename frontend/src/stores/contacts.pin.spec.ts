import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useContactsStore } from './contacts'

const pin = vi.fn()
const unpin = vi.fn()
vi.mock('@/services/api', () => ({
  contactsService: { pin: (...a: unknown[]) => pin(...a), unpin: (...a: unknown[]) => unpin(...a) },
  messagesService: {},
}))

const c = (id: string, last: string, extra: Record<string, unknown> = {}) =>
  ({ id, last_message_at: last, unread_count: 0, ...extra }) as any

describe('contacts store: pinned conversations', () => {
  beforeEach(() => {
    pin.mockReset().mockResolvedValue({})
    unpin.mockReset().mockResolvedValue({})
    setActivePinia(createPinia())
  })

  it('lists pinned first (most recently pinned on top), the rest by last message', () => {
    const s = useContactsStore()
    s.contacts = [
      c('old', '2026-01-01T10:00:00Z'),
      c('new', '2026-01-03T10:00:00Z'),
      c('p1', '2026-01-01T09:00:00Z', { is_pinned: true, pinned_at: '2026-01-05T10:00:00Z' }),
      c('p2', '2026-01-01T08:00:00Z', { is_pinned: true, pinned_at: '2026-01-06T10:00:00Z' }),
    ]
    expect(s.sortedContacts.map(x => x.id)).toEqual(['p2', 'p1', 'new', 'old'])
  })

  it('setPinned updates the row on success and moves it to the top', async () => {
    const s = useContactsStore()
    s.contacts = [c('a', '2026-01-03T10:00:00Z'), c('b', '2026-01-01T10:00:00Z')]
    await s.setPinned('b', true)
    expect(pin).toHaveBeenCalledWith('b')
    expect(s.sortedContacts.map(x => x.id)).toEqual(['b', 'a'])
    await s.setPinned('b', false)
    expect(unpin).toHaveBeenCalledWith('b')
    expect(s.sortedContacts.map(x => x.id)).toEqual(['a', 'b'])
  })

  it('a refused pin (limit) leaves the list untouched and rethrows', async () => {
    pin.mockRejectedValue({ response: { status: 409 } })
    const s = useContactsStore()
    s.contacts = [c('a', '2026-01-03T10:00:00Z')]
    await expect(s.setPinned('a', true)).rejects.toBeTruthy()
    expect(s.contacts[0].is_pinned).toBeFalsy()
  })
})
