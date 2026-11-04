import { describe, expect, it } from 'vitest'
import { canSendProtocol, canSendRegistrationMessage, sendOutcome } from './occurrence-customer-messaging'

describe('occurrence customer messaging', () => {
  it('offers the protocol only when the server says the person may send it', () => {
    expect(canSendProtocol({ send_protocol: true, reply: true })).toBe(true)
    expect(canSendProtocol({ send_protocol: false, reply: true })).toBe(false)
  })

  it('offers nothing when the server did not say, or said nothing about it', () => {
    expect(canSendProtocol(undefined)).toBe(false)
    expect(canSendProtocol({})).toBe(false)
    expect(canSendProtocol({ reply: true })).toBe(false)
  })

  it('the registration message needs the permission to write to customers', () => {
    expect(canSendRegistrationMessage(true)).toBe(true)
    expect(canSendRegistrationMessage(false)).toBe(false)
  })
})

describe('outcome of a protocol send', () => {
  const body = (result?: string) => ({ status: 'success', data: { result, sent: result === 'sent' } })

  it('reads an unconfirmed result as uncertain, never as sent', () => {
    expect(sendOutcome(body('uncertain'))).toBe('uncertain')
  })

  it('reads a confirmed send as sent', () => {
    expect(sendOutcome(body('sent'))).toBe('sent')
  })

  it('keeps working with an answer from a server that does not say (older body)', () => {
    expect(sendOutcome({ status: 'success', data: { sent: true } })).toBe('sent')
    expect(sendOutcome(undefined)).toBe('sent')
    expect(sendOutcome(null)).toBe('sent')
  })
})
