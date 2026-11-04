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

  it('after the application refused (its own error envelope), the next try is a new intention', async () => {
    sendProtocol.mockRejectedValueOnce({ response: { status: 403, data: { status: 'error', message: 'no' } } }).mockResolvedValueOnce(answer(200, 'sent'))
    const s = useOccurrencesStore()
    await expect(s.sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy()
    await s.sendRegistrationMessage('o1', 'oi')
    expect(keyOf(1)).not.toBe(keyOf(0))
  })

  it('a different case is a different intention', async () => {
    sendProtocol.mockResolvedValue(answer(202, 'uncertain'))
    const s = useOccurrencesStore()
    await s.sendRegistrationMessage('o1', 'oi')
    await s.sendRegistrationMessage('o2', 'oi')
    expect(keyOf(0)).not.toBe(keyOf(1))
  })

  it('a different text is a new intention once the earlier one is resolved', async () => {
    sendProtocol.mockResolvedValue(answer(200, 'sent'))
    const s = useOccurrencesStore()
    await s.sendRegistrationMessage('o1', 'oi')
    await s.sendRegistrationMessage('o1', 'outro')
    expect(keyOf(1)).not.toBe(keyOf(0))
  })
})

// Regression for the review of PR 143 (findings 1 and 2).
describe('protocol send: an answer that does not close the intention keeps the key', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    sendProtocol.mockReset()
  })
  const proxy = (status: number, data: unknown = '<html>bad gateway</html>') => ({ response: { status, data } })

  for (const status of [502, 503, 504]) {
    it(`a proxy ${status} (no application envelope) keeps the key: the retry is the SAME send`, async () => {
      sendProtocol.mockRejectedValueOnce(proxy(status)).mockResolvedValueOnce(answer(200, 'sent'))
      const s = useOccurrencesStore()
      await expect(s.sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy()
      await s.sendRegistrationMessage('o1', 'oi')
      expect(sendProtocol.mock.calls[1][2]).toBe(sendProtocol.mock.calls[0][2])
    })
  }

  it('a 5xx with an empty or JSON body without our envelope also keeps the key', async () => {
    sendProtocol.mockRejectedValueOnce(proxy(502, {})).mockResolvedValueOnce(answer(200, 'sent'))
    const s = useOccurrencesStore()
    await expect(s.sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy()
    await s.sendRegistrationMessage('o1', 'oi')
    expect(sendProtocol.mock.calls[1][2]).toBe(sendProtocol.mock.calls[0][2])
  })

  it('a timeout (no response) keeps the key', async () => {
    sendProtocol.mockRejectedValueOnce({ code: 'ECONNABORTED' }).mockResolvedValueOnce(answer(200, 'sent'))
    const s = useOccurrencesStore()
    await expect(s.sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy()
    await s.sendRegistrationMessage('o1', 'oi')
    expect(sendProtocol.mock.calls[1][2]).toBe(sendProtocol.mock.calls[0][2])
  })

  it('the application own proven failure (502 carrying the send_id) closes the intention', async () => {
    sendProtocol
      .mockRejectedValueOnce({ response: { status: 502, data: { status: 'error', data: { result: 'failed', send_id: 's1' } } } })
      .mockResolvedValueOnce(answer(200, 'sent'))
    const s = useOccurrencesStore()
    await expect(s.sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy()
    await s.sendRegistrationMessage('o1', 'oi')
    expect(sendProtocol.mock.calls[1][2]).not.toBe(sendProtocol.mock.calls[0][2])
  })

  it('a proxy 4xx (413, 429: no application envelope) does not close the intention either', async () => {
    sendProtocol.mockRejectedValueOnce(proxy(429)).mockResolvedValueOnce(answer(200, 'sent'))
    const s = useOccurrencesStore()
    await expect(s.sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy()
    await s.sendRegistrationMessage('o1', 'oi')
    expect(sendProtocol.mock.calls[1][2]).toBe(sendProtocol.mock.calls[0][2])
  })

  it('editing the text while the first send is unresolved does NOT mint a second key (nothing is posted)', async () => {
    sendProtocol.mockRejectedValueOnce({ code: 'ECONNABORTED' })
    const s = useOccurrencesStore()
    await expect(s.sendRegistrationMessage('o1', 'texto original')).rejects.toBeTruthy()
    await expect(s.sendRegistrationMessage('o1', 'texto editado')).rejects.toMatchObject({ name: 'ProtocolSendPendingError' })
    expect(sendProtocol).toHaveBeenCalledTimes(1)
  })

  it('editing the text while the first send is pending (202) does not post either', async () => {
    sendProtocol.mockResolvedValueOnce(answer(202, 'uncertain'))
    const s = useOccurrencesStore()
    await s.sendRegistrationMessage('o1', 'texto original')
    await expect(s.sendRegistrationMessage('o1', 'texto editado')).rejects.toMatchObject({ name: 'ProtocolSendPendingError' })
    expect(sendProtocol).toHaveBeenCalledTimes(1)
  })

  it('same text after the inconclusive try reuses the key, and a conclusive answer then frees the next intention', async () => {
    sendProtocol.mockRejectedValueOnce(proxy(504)).mockResolvedValueOnce(answer(200, 'sent')).mockResolvedValueOnce(answer(200, 'sent'))
    const s = useOccurrencesStore()
    await expect(s.sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy()
    await s.sendRegistrationMessage('o1', 'oi')
    await s.sendRegistrationMessage('o1', 'outro texto')
    const k = (i: number) => sendProtocol.mock.calls[i][2]
    expect(k(1)).toBe(k(0))
    expect(k(2)).not.toBe(k(0))
  })
})
