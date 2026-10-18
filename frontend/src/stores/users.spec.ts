import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useUsersStore } from './users'

vi.mock('@/services/api', () => ({ usersService: {} }))

const baseUser = {
  id: 'u1',
  email: 'a@b.c',
  full_name: 'Ana',
  is_active: true,
  organization_id: 'o1',
  created_at: '2026-10-16T00:00:00Z',
  updated_at: '2026-10-16T00:00:00Z',
}

describe('users store: presence state from the backend', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('keeps the backend state and the instant it began; the client never times it', () => {
    const store = useUsersStore()
    store.users = [{ ...baseUser, is_available: true, is_online: true, presence_state: 'online' }]

    store.applyAvailability('u1', false, 'away', '2026-10-16T14:10:00.000Z')

    const u = store.users[0]
    expect(u.is_available).toBe(false)
    expect(u.presence_state).toBe('away')
    expect(u.status_since).toBe('2026-10-16T14:10:00.000Z')
  })

  it('offline wins: a presence event carries the combined state', () => {
    const store = useUsersStore()
    store.users = [{ ...baseUser, is_online: true, presence_state: 'online', status_since: 'old' }]

    store.applyPresence('u1', false, 'offline', '2026-10-16T10:32:15.000Z')

    const u = store.users[0]
    expect(u.is_online).toBe(false)
    expect(u.presence_state).toBe('offline')
    expect(u.status_since).toBe('2026-10-16T10:32:15.000Z')
  })

  it('a null since means the log cannot say: it is stored as null, not as "now"', () => {
    const store = useUsersStore()
    store.users = [{ ...baseUser, presence_state: 'away', status_since: '2026-10-16T14:10:00.000Z' }]

    store.applyPresence('u1', true, 'online', null)

    expect(store.users[0].presence_state).toBe('online')
    expect(store.users[0].status_since).toBeNull()
  })

  it('older payloads without a state still update the original fields', () => {
    const store = useUsersStore()
    store.users = [{ ...baseUser, is_online: true, is_available: true, presence_state: 'online', status_since: 'x' }]

    store.applyPresence('u1', false)
    store.applyAvailability('u1', false)

    const u = store.users[0]
    expect(u.is_online).toBe(false)
    expect(u.is_available).toBe(false)
    expect(u.presence_state).toBe('online') // untouched: no state was reported
    expect(u.status_since).toBe('x')
  })

  it('ignores an unknown state value', () => {
    const store = useUsersStore()
    store.users = [{ ...baseUser, presence_state: 'online' }]

    store.applyPresence('u1', true, 'busy', 'whenever')

    expect(store.users[0].presence_state).toBe('online')
  })

  it('ignores users that are not in the loaded list', () => {
    const store = useUsersStore()
    store.users = [{ ...baseUser }]
    expect(() => store.applyPresence('nope', false, 'offline', 'x')).not.toThrow()
    expect(store.users[0].presence_state).toBeUndefined()
  })
})
