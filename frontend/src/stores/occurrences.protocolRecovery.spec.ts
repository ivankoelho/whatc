// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { ref } from 'vue'

// Coming back to a protocol send whose outcome the page never saw: the browser remembers the key
// (scoped by organization, user and case), asks the server, and NEVER posts a new send to do it.
const sendProtocol = vi.fn()
const getProtocolSend = vi.fn()
const getProtocolSendByKey = vi.fn()
vi.mock('@/services/api', () => ({
  occurrencesService: {
    sendProtocol: (...a: unknown[]) => sendProtocol(...a),
    getProtocolSend: (...a: unknown[]) => getProtocolSend(...a),
    getProtocolSendByKey: (...a: unknown[]) => getProtocolSendByKey(...a),
    listEvents: vi.fn(async () => ({ data: { data: { events: [] } } })),
  },
  occurrenceProcessesService: {},
}))
vi.mock('vue-sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock('@/i18n', () => ({ i18n: { global: { t: (k: string) => k } } }))

const org = ref('org-1')
const user = ref<{ id: string } | null>({ id: 'user-1' })
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    get organizationId() { return org.value },
    get user() { return user.value },
  }),
}))

import { useOccurrencesStore } from './occurrences'
import { RECOVERY_TTL_MS, IN_FLIGHT_GRACE_MS, recordStorageKey } from '@/lib/protocol-send-recovery'

const post = (status: number, result: string, sendId = 'send-1') => ({ status, data: { status: 'success', data: { result, send_id: sendId } } })
const state = (s: string, id = 'send-1') => ({ data: { data: { state: s, send_id: id, sent: s === 'sent', confirmed: s === 'sent' || s === 'failed' } } })
const keyOf = (n: number) => sendProtocol.mock.calls[n][2] as string
// A refresh: a brand new store (memory gone), the same browser storage.
const refresh = () => setActivePinia(createPinia())
const scope = (userId = 'user-1', organizationId = 'org-1', occurrenceId = 'o1') => ({ organizationId, userId, occurrenceId })

describe('protocol send: recovery after refresh', () => {
  beforeEach(() => {
    localStorage.clear()
    org.value = 'org-1'
    user.value = { id: 'user-1' }
    sendProtocol.mockReset()
    getProtocolSend.mockReset()
    getProtocolSendByKey.mockReset()
    vi.useRealTimers()
    setActivePinia(createPinia())
  })

  it('refresh after 202: asks by send_id, shows the state, posts nothing', async () => {
    sendProtocol.mockResolvedValue(post(202, 'uncertain'))
    expect(await useOccurrencesStore().sendRegistrationMessage('o1', 'oi')).toBe('uncertain')
    refresh()
    getProtocolSend.mockResolvedValue(state('uncertain'))
    expect(await useOccurrencesStore().recoverProtocolSend('o1')).toBe('uncertain')
    expect(getProtocolSend).toHaveBeenCalledWith('o1', 'send-1')
    expect(sendProtocol).toHaveBeenCalledTimes(1)
  })

  it('refresh after a lost HTTP answer: asks by key, learns the send_id, posts nothing', async () => {
    sendProtocol.mockRejectedValue(new Error('Network Error'))
    await expect(useOccurrencesStore().sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy()
    refresh()
    getProtocolSendByKey.mockResolvedValue(state('uncertain', 'send-9'))
    expect(await useOccurrencesStore().recoverProtocolSend('o1')).toBe('uncertain')
    expect(getProtocolSendByKey).toHaveBeenCalledWith('o1', keyOf(0))
    expect(sendProtocol).toHaveBeenCalledTimes(1)
    refresh() // the next visit already knows the send
    getProtocolSend.mockResolvedValue(state('uncertain', 'send-9'))
    await useOccurrencesStore().recoverProtocolSend('o1')
    expect(getProtocolSend).toHaveBeenCalledWith('o1', 'send-9')
  })

  it('shows each state the server reports; a resolved one is forgotten', async () => {
    for (const [s, forgotten] of [['processing', false], ['uncertain', false], ['sent', true], ['failed', true]] as const) {
      localStorage.clear()
      refresh()
      sendProtocol.mockResolvedValue(post(202, 'uncertain'))
      await useOccurrencesStore().sendRegistrationMessage('o1', 'oi')
      refresh()
      getProtocolSend.mockResolvedValue(state(s))
      const st = useOccurrencesStore()
      expect(await st.recoverProtocolSend('o1')).toBe(s)
      expect(localStorage.getItem(recordStorageKey(scope())) === null).toBe(forgotten)
      if (forgotten) expect(await st.recoverProtocolSend('o1')).toBeNull()
    }
  })

  it('while the send is unresolved a second click replays the SAME key (never a new send)', async () => {
    sendProtocol.mockResolvedValue(post(202, 'uncertain'))
    await useOccurrencesStore().sendRegistrationMessage('o1', 'oi')
    refresh()
    await useOccurrencesStore().sendRegistrationMessage('o1', 'oi')
    expect(keyOf(1)).toBe(keyOf(0))
  })

  it('several cases never mix: each has its own record and answer', async () => {
    sendProtocol.mockResolvedValueOnce(post(202, 'uncertain', 'send-A')).mockResolvedValueOnce(post(202, 'uncertain', 'send-B'))
    const s = useOccurrencesStore()
    await s.sendRegistrationMessage('oA', 'oi')
    await s.sendRegistrationMessage('oB', 'oi')
    expect(keyOf(0)).not.toBe(keyOf(1))
    refresh()
    getProtocolSend.mockImplementation(async (_o: string, id: string) => state(id === 'send-A' ? 'sent' : 'uncertain', id))
    const r = useOccurrencesStore()
    expect(await r.recoverProtocolSend('oA')).toBe('sent')
    expect(await r.recoverProtocolSend('oB')).toBe('uncertain')
    expect(getProtocolSend).toHaveBeenCalledWith('oA', 'send-A')
    expect(getProtocolSend).toHaveBeenCalledWith('oB', 'send-B')
  })

  it('another user or organization on the same browser sees nothing of it', async () => {
    sendProtocol.mockResolvedValue(post(202, 'uncertain'))
    await useOccurrencesStore().sendRegistrationMessage('o1', 'oi')
    refresh()
    user.value = { id: 'user-2' }
    expect(await useOccurrencesStore().recoverProtocolSend('o1')).toBeNull()
    user.value = { id: 'user-1' }
    org.value = 'org-2'
    expect(await useOccurrencesStore().recoverProtocolSend('o1')).toBeNull()
    expect(getProtocolSend).not.toHaveBeenCalled()
    expect(getProtocolSendByKey).not.toHaveBeenCalled()
    // and a new send by the other user does not reuse the first user's key
    user.value = { id: 'user-2' }
    org.value = 'org-1'
    await useOccurrencesStore().sendRegistrationMessage('o1', 'oi')
    expect(keyOf(1)).not.toBe(keyOf(0))
  })

  it('a record expires after 24 h: no query, and the next send is a new intention', async () => {
    vi.useFakeTimers()
    sendProtocol.mockResolvedValue(post(202, 'uncertain'))
    await useOccurrencesStore().sendRegistrationMessage('o1', 'oi')
    vi.setSystemTime(Date.now() + RECOVERY_TTL_MS + 1000)
    refresh()
    expect(await useOccurrencesStore().recoverProtocolSend('o1')).toBeNull()
    expect(getProtocolSend).not.toHaveBeenCalled()
    await useOccurrencesStore().sendRegistrationMessage('o1', 'oi')
    expect(keyOf(1)).not.toBe(keyOf(0))
  })

  it('no claim found: still "processing" while a request may be in flight, forgotten after', async () => {
    vi.useFakeTimers()
    sendProtocol.mockRejectedValue(new Error('Network Error'))
    await expect(useOccurrencesStore().sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy()
    refresh()
    getProtocolSendByKey.mockRejectedValue({ response: { status: 404 } })
    expect(await useOccurrencesStore().recoverProtocolSend('o1')).toBe('processing')
    vi.setSystemTime(Date.now() + IN_FLIGHT_GRACE_MS + 1000)
    expect(await useOccurrencesStore().recoverProtocolSend('o1')).toBeNull()
    expect(sendProtocol).toHaveBeenCalledTimes(1)
  })

  it('a refused query (403) or no network keeps the record and says nothing false', async () => {
    sendProtocol.mockResolvedValue(post(202, 'uncertain'))
    await useOccurrencesStore().sendRegistrationMessage('o1', 'oi')
    refresh()
    getProtocolSend.mockRejectedValue({ response: { status: 403 } })
    await expect(useOccurrencesStore().recoverProtocolSend('o1')).rejects.toBeTruthy()
    getProtocolSend.mockResolvedValue(state('sent'))
    expect(await useOccurrencesStore().recoverProtocolSend('o1')).toBe('sent')
  })

  it('stores ids only: never the text of the message', async () => {
    sendProtocol.mockResolvedValue(post(202, 'uncertain'))
    await useOccurrencesStore().sendRegistrationMessage('o1', 'texto confidencial do cliente')
    const raw = localStorage.getItem(recordStorageKey(scope()))!
    expect(raw).not.toContain('confidencial')
    expect(Object.keys(JSON.parse(raw)).sort()).toEqual(['key', 'savedAt', 'sendId', 'textHash'])
  })
})

// Regression for the review of PR 143 (findings 1 and 2).
describe('protocol send: proxy 5xx, timeout and edited text', () => {
  beforeEach(() => {
    localStorage.clear()
    org.value = 'org-1'
    user.value = { id: 'user-1' }
    sendProtocol.mockReset()
    getProtocolSend.mockReset()
    getProtocolSendByKey.mockReset()
    vi.useRealTimers()
    setActivePinia(createPinia())
  })
  const proxy502 = { response: { status: 502, data: '<html>502 Bad Gateway</html>' } }

  it('a proxy 502 mid-send: the record survives, a refresh asks by key and posts nothing', async () => {
    sendProtocol.mockRejectedValue(proxy502)
    await expect(useOccurrencesStore().sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy()
    expect(localStorage.getItem(recordStorageKey(scope()))).not.toBeNull()
    refresh()
    getProtocolSendByKey.mockResolvedValue(state('uncertain', 'send-7'))
    expect(await useOccurrencesStore().recoverProtocolSend('o1')).toBe('uncertain')
    expect(getProtocolSendByKey).toHaveBeenCalledWith('o1', keyOf(0))
    expect(sendProtocol).toHaveBeenCalledTimes(1)
  })

  it('after the proxy 502 the server had in fact sent it: recovery reports sent and the record is forgotten', async () => {
    sendProtocol.mockRejectedValue(proxy502)
    await expect(useOccurrencesStore().sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy()
    getProtocolSendByKey.mockResolvedValue(state('sent', 'send-7'))
    expect(await useOccurrencesStore().recoverProtocolSend('o1')).toBe('sent')
    expect(localStorage.getItem(recordStorageKey(scope()))).toBeNull()
  })

  it('timeout, then edit the text and send: refused before any POST; the original key stays', async () => {
    sendProtocol.mockRejectedValue({ code: 'ECONNABORTED' })
    const s = useOccurrencesStore()
    await expect(s.sendRegistrationMessage('o1', 'original')).rejects.toBeTruthy()
    await expect(s.sendRegistrationMessage('o1', 'editado')).rejects.toMatchObject({ name: 'ProtocolSendPendingError' })
    expect(sendProtocol).toHaveBeenCalledTimes(1)
    refresh() // even after a refresh the pending record blocks an edited text
    await expect(useOccurrencesStore().sendRegistrationMessage('o1', 'editado')).rejects.toMatchObject({ name: 'ProtocolSendPendingError' })
    expect(sendProtocol).toHaveBeenCalledTimes(1)
    expect(JSON.parse(localStorage.getItem(recordStorageKey(scope()))!).key).toBe(keyOf(0))
  })

  it('once the server says failed, the edited text is a new intention with a new key', async () => {
    sendProtocol.mockRejectedValueOnce({ code: 'ECONNABORTED' }).mockResolvedValueOnce(post(200, 'sent', 'send-2'))
    const s = useOccurrencesStore()
    await expect(s.sendRegistrationMessage('o1', 'original')).rejects.toBeTruthy()
    getProtocolSendByKey.mockResolvedValue(state('failed', 'send-1'))
    expect(await s.recoverProtocolSend('o1')).toBe('failed')
    await s.sendRegistrationMessage('o1', 'editado')
    expect(keyOf(1)).not.toBe(keyOf(0))
  })

  it('repeat after a conclusive 200: a new intention with a new key', async () => {
    sendProtocol.mockResolvedValue(post(200, 'sent'))
    const s = useOccurrencesStore()
    await s.sendRegistrationMessage('o1', 'oi')
    await s.sendRegistrationMessage('o1', 'oi')
    expect(keyOf(1)).not.toBe(keyOf(0))
  })
})

// Regression for the second review of PR 143 (M1 and L1).
describe('protocol send: a refusal of a RETRY does not close an unresolved intention', () => {
  beforeEach(() => {
    localStorage.clear()
    org.value = 'org-1'
    user.value = { id: 'user-1' }
    sendProtocol.mockReset()
    getProtocolSend.mockReset()
    getProtocolSendByKey.mockReset()
    vi.useRealTimers()
    setActivePinia(createPinia())
  })
  const refusal = (status: number, error_type = '') => ({ response: { status, data: { status: 'error', message: 'refused', error_type } } })
  const rec = () => JSON.parse(localStorage.getItem(recordStorageKey(scope()))!)

  it('the reported scenario: timeout, recovery fails, contact data changes, retry gets 409, the key survives and no new send is possible', async () => {
    sendProtocol.mockRejectedValueOnce({ code: 'ECONNABORTED' }).mockRejectedValueOnce(refusal(409, 'idempotency_key_reused'))
    const s = useOccurrencesStore()
    await expect(s.sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy() // timeout, the server may have sent
    getProtocolSendByKey.mockRejectedValue(new Error('Network Error'))
    await expect(s.recoverProtocolSend('o1')).rejects.toBeTruthy() // recovery inconclusive: the button would be free
    await expect(s.sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy() // webhook changed the BSUID: 409
    expect(s.hasPendingProtocolSend('o1')).toBe(true)
    // the next click is still the SAME intention
    sendProtocol.mockRejectedValueOnce({ code: 'ECONNABORTED' })
    await expect(s.sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy()
    expect(keyOf(2)).toBe(keyOf(0))
    expect(keyOf(1)).toBe(keyOf(0))
  })

  for (const [status, type] of [[401, ''], [403, ''], [404, ''], [409, 'idempotency_key_reused'], [429, '']] as const) {
    it(`a ${status} on a retry of an unresolved key keeps the record, the key and the lock on other text`, async () => {
      sendProtocol.mockRejectedValueOnce({ code: 'ECONNABORTED' }).mockRejectedValueOnce(refusal(status, type))
      const s = useOccurrencesStore()
      await expect(s.sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy()
      await expect(s.sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy()
      expect(s.hasPendingProtocolSend('o1')).toBe(true)
      expect(rec().key).toBe(keyOf(0))
      await expect(s.sendRegistrationMessage('o1', 'outro')).rejects.toMatchObject({ name: 'ProtocolSendPendingError' })
      expect(sendProtocol).toHaveBeenCalledTimes(2)
    })
  }

  it('the same refusal on the FIRST request under a key still closes it (nothing was started)', async () => {
    for (const [status, type] of [[403, ''], [409, 'idempotency_key_reused'], [429, '']] as const) {
      localStorage.clear()
      setActivePinia(createPinia())
      sendProtocol.mockReset()
      sendProtocol.mockRejectedValueOnce(refusal(status, type)).mockResolvedValueOnce(post(200, 'sent'))
      const s = useOccurrencesStore()
      await expect(s.sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy()
      expect(s.hasPendingProtocolSend('o1')).toBe(false)
      await s.sendRegistrationMessage('o1', 'outro texto') // free to edit and send a new intention
      expect(keyOf(1)).not.toBe(keyOf(0))
    }
  })

  it('a conclusive answer on the retry (the stored result carries the send_id) closes the intention and frees the next one', async () => {
    sendProtocol
      .mockRejectedValueOnce({ code: 'ECONNABORTED' })
      .mockRejectedValueOnce({ response: { status: 502, data: { status: 'error', data: { result: 'failed', send_id: 's1' } } } })
      .mockResolvedValueOnce(post(200, 'sent', 's2'))
    const s = useOccurrencesStore()
    await expect(s.sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy()
    await expect(s.sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy()
    expect(s.hasPendingProtocolSend('o1')).toBe(false)
    await s.sendRegistrationMessage('o1', 'texto editado')
    expect(keyOf(2)).not.toBe(keyOf(0))
  })

  it('the server saying failed (recovery) frees the edit after a refused retry', async () => {
    sendProtocol.mockRejectedValueOnce({ code: 'ECONNABORTED' }).mockRejectedValueOnce(refusal(409, 'idempotency_key_reused')).mockResolvedValueOnce(post(200, 'sent', 's2'))
    const s = useOccurrencesStore()
    await expect(s.sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy()
    await expect(s.sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy()
    getProtocolSendByKey.mockResolvedValue(state('failed', 's1'))
    expect(await s.recoverProtocolSend('o1')).toBe('failed')
    await s.sendRegistrationMessage('o1', 'editado')
    expect(keyOf(2)).not.toBe(keyOf(0))
  })

  it('L1: a retry renews savedAt, so the 60 s grace counts from the latest request; the record does not expire early', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-11-04T12:00:00Z'))
    sendProtocol.mockRejectedValue({ code: 'ECONNABORTED' })
    const s = useOccurrencesStore()
    await expect(s.sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy()
    const first = rec().savedAt
    vi.setSystemTime(new Date('2026-11-04T12:05:00Z')) // five minutes later, a retry
    await expect(s.sendRegistrationMessage('o1', 'oi')).rejects.toBeTruthy()
    expect(rec().savedAt).toBeGreaterThan(first)
    expect(rec().key).toBe(keyOf(0))
    // 10 s after the retry the server still has no claim: still "in progress", the key is NOT released
    vi.setSystemTime(new Date('2026-11-04T12:05:10Z'))
    getProtocolSendByKey.mockRejectedValue({ response: { status: 404 } })
    expect(await s.recoverProtocolSend('o1')).toBe('processing')
    expect(s.hasPendingProtocolSend('o1')).toBe(true)
    // and once the grace after the LAST request has passed, it is released
    vi.setSystemTime(new Date(Date.parse('2026-11-04T12:05:00Z') + IN_FLIGHT_GRACE_MS + 1000))
    expect(await s.recoverProtocolSend('o1')).toBeNull()
    vi.useRealTimers()
  })
})
