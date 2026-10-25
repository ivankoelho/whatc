import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useContactsStore } from './contacts'

vi.mock('@/services/api', () => ({ contactsService: {}, messagesService: {} }))

const contact = () => ({
  id: 'c1', last_message_at: '2026-10-01T10:00:00Z', unread_count: 0,
}) as any

const msg = (extra: Record<string, unknown>) => ({
  id: 'm' + Math.random(), contact_id: 'c1', direction: 'outgoing', message_type: 'template',
  content: { body: 'x' }, status: 'sent', created_at: '2026-10-20T10:00:00Z', ...extra,
}) as any

describe('contacts store: verification-code messages stay out of the inbox', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('does not promote the conversation, but still adds the message to history', () => {
    const store = useContactsStore()
    store.contacts = [contact()]
    store.addMessage(msg({ interactive_data: { type: 'auth_code' } }))

    expect(store.contacts[0].last_message_at).toBe('2026-10-01T10:00:00Z')
    expect(store.contacts[0].unread_count).toBe(0)
    expect(store.messages).toHaveLength(1)
  })

  it('a normal template still promotes the conversation', () => {
    const store = useContactsStore()
    store.contacts = [contact()]
    store.addMessage(msg({ interactive_data: { type: 'button', buttons: [] } }))

    expect(store.contacts[0].last_message_at).toBe('2026-10-20T10:00:00Z')
  })

  it('an older payload without the marker keeps the previous behaviour', () => {
    const store = useContactsStore()
    store.contacts = [contact()]
    store.addMessage(msg({}))

    expect(store.contacts[0].last_message_at).toBe('2026-10-20T10:00:00Z')
  })
})
