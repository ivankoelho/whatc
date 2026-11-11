import { describe, it, expect } from 'vitest'
import { canChangeConversationStatus, statusChangeErrorKey } from './conversationStatus'

const grants = (...pairs: string[]) => (resource: string, action: string) => pairs.includes(`${resource}:${action}`)

describe('canChangeConversationStatus', () => {
  it('lets an attendant (chat:write) conclude, without contacts:write', () => {
    expect(canChangeConversationStatus(grants('chat:write', 'contacts:read'))).toBe(true)
  })

  it('keeps contacts:write working on its own', () => {
    expect(canChangeConversationStatus(grants('contacts:write'))).toBe(true)
  })

  it('does not offer the action to people who can neither answer customers nor edit contacts', () => {
    expect(canChangeConversationStatus(grants('chat:read', 'contacts:read'))).toBe(false)
    expect(canChangeConversationStatus(grants())).toBe(false)
  })
})

describe('statusChangeErrorKey', () => {
  it('says "not allowed" for a 403, not "try again"', () => {
    expect(statusChangeErrorKey({ response: { status: 403 } })).toBe('chat.statusChangeForbidden')
  })

  it('keeps the generic message for every other failure', () => {
    expect(statusChangeErrorKey({ response: { status: 500 } })).toBe('chat.statusChangeFailed')
    expect(statusChangeErrorKey(new Error('network'))).toBe('chat.statusChangeFailed')
    expect(statusChangeErrorKey(null)).toBe('chat.statusChangeFailed')
  })
})
