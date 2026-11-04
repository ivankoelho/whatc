import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

// How the store reads the answer of send-protocol: 200 sent, 202 not confirmed (it may have been
// delivered: neither a success nor a failure), 422 closed window, anything else an error.
const sendProtocol = vi.fn()
vi.mock('@/services/api', () => ({
  occurrencesService: {
    sendProtocol: (...a: unknown[]) => sendProtocol(...a),
    listEvents: vi.fn(async () => ({ data: { data: { events: [] } } })),
  },
  occurrenceProcessesService: {},
}))
vi.mock('vue-sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock('@/i18n', () => ({ i18n: { global: { t: (k: string) => k } } }))

import { useOccurrencesStore } from './occurrences'

const answer = (status: number, result?: string) => ({ status, data: { status: 'success', data: { result, sent: result === 'sent' } } })

describe('protocol send: what the store tells the screens', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    sendProtocol.mockReset()
  })

  it('registration message: 200 is sent', async () => {
    sendProtocol.mockResolvedValue(answer(200, 'sent'))
    expect(await useOccurrencesStore().sendRegistrationMessage('o1', 'oi')).toBe('sent')
  })

  it('registration message: 202 uncertain is NOT sent and NOT a failure', async () => {
    sendProtocol.mockResolvedValue(answer(202, 'uncertain'))
    expect(await useOccurrencesStore().sendRegistrationMessage('o1', 'oi')).toBe('uncertain')
  })

  it('registration message: a closed window (422) is reported as such', async () => {
    sendProtocol.mockRejectedValue({ response: { status: 422 } })
    expect(await useOccurrencesStore().sendRegistrationMessage('o1', 'oi')).toBe('window_closed')
  })

  it('registration message: a real failure still throws, so it is shown as an error', async () => {
    sendProtocol.mockRejectedValue({ response: { status: 502, data: { error_type: '' } } })
    await expect(useOccurrencesStore().sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy()
  })

  it('header button: 202 uncertain comes back as uncertain, never as sent', async () => {
    sendProtocol.mockResolvedValue(answer(202, 'uncertain'))
    expect(await useOccurrencesStore().sendProtocol('o1')).toBe('uncertain')
  })

  it('header button: 200 comes back as sent', async () => {
    sendProtocol.mockResolvedValue(answer(200, 'sent'))
    expect(await useOccurrencesStore().sendProtocol('o1')).toBe('sent')
  })
})

describe('protocol send: one Idempotency-Key per intention', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    sendProtocol.mockReset()
  })
  const keyOf = (call: number) => sendProtocol.mock.calls[call][2] as string

  it('always sends a key (a UUID)', async () => {
    sendProtocol.mockResolvedValue(answer(200, 'sent'))
    await useOccurrencesStore().sendRegistrationMessage('o1', 'oi')
    expect(keyOf(0)).toMatch(/^[0-9a-f-]{36}$/)
  })

  it('a new intention after a resolved send uses a new key', async () => {
    sendProtocol.mockResolvedValue(answer(200, 'sent'))
    const s = useOccurrencesStore()
    await s.sendRegistrationMessage('o1', 'oi')
    await s.sendRegistrationMessage('o1', 'oi')
    expect(keyOf(1)).not.toBe(keyOf(0))
  })

  it('a retry after a lost answer (no response at all) reuses the key', async () => {
    sendProtocol.mockRejectedValueOnce(new Error('Network Error')).mockResolvedValueOnce(answer(200, 'sent'))
    const s = useOccurrencesStore()
    await expect(s.sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy()
    await s.sendRegistrationMessage('o1', 'oi')
    expect(keyOf(1)).toBe(keyOf(0))
  })

  it('after 202 (not confirmed) the same intention keeps its key, and "processing" is not a success', async () => {
    sendProtocol.mockResolvedValue(answer(202, 'processing'))
    const s = useOccurrencesStore()
    expect(await s.sendRegistrationMessage('o1', 'oi')).toBe('uncertain')
    await s.sendRegistrationMessage('o1', 'oi')
    expect(keyOf(1)).toBe(keyOf(0))
  })

  it('after the server refused (an answer), the next try is a new intention', async () => {
    sendProtocol.mockRejectedValueOnce({ response: { status: 502, data: {} } }).mockResolvedValueOnce(answer(200, 'sent'))
    const s = useOccurrencesStore()
    await expect(s.sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy()
    await s.sendRegistrationMessage('o1', 'oi')
    expect(keyOf(1)).not.toBe(keyOf(0))
  })

  it('a different text or case is a different intention', async () => {
    sendProtocol.mockResolvedValue(answer(202, 'uncertain'))
    const s = useOccurrencesStore()
    await s.sendRegistrationMessage('o1', 'oi')
    await s.sendRegistrationMessage('o1', 'outro')
    await s.sendRegistrationMessage('o2', 'oi')
    expect(new Set([keyOf(0), keyOf(1), keyOf(2)]).size).toBe(3)
  })
})
