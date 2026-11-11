import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useContactsStore } from './contacts'

const statusCounts = vi.fn()
vi.mock('@/services/api', () => ({
  contactsService: { statusCounts: (...a: unknown[]) => statusCounts(...a) },
  messagesService: {},
}))

const reply = (n: number) => ({ data: { data: { new: n } } })
const deferred = <T,>() => {
  let resolve!: (v: T) => void
  const promise = new Promise<T>(r => (resolve = r))
  return { promise, resolve }
}

describe('contacts store: "New" counter follows the server', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    statusCounts.mockReset()
    setActivePinia(createPinia())
  })
  afterEach(() => vi.useRealTimers())

  it('a burst of hints collapses into one request (debounce)', async () => {
    statusCounts.mockResolvedValue(reply(4))
    const store = useContactsStore()
    for (let i = 0; i < 5; i++) store.scheduleStatusCountsRefresh()
    expect(statusCounts).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(600)
    expect(statusCounts).toHaveBeenCalledTimes(1)
    expect(store.newCount).toBe(4)
  })

  it('a slow older response never overwrites a newer one', async () => {
    const slow = deferred<ReturnType<typeof reply>>()
    statusCounts.mockReturnValueOnce(slow.promise).mockResolvedValueOnce(reply(2))
    const store = useContactsStore()
    const first = store.fetchStatusCounts() // slow, will answer last with a stale 9
    await store.fetchStatusCounts() // newer, answers 2
    expect(store.newCount).toBe(2)
    slow.resolve(reply(9))
    await first
    expect(store.newCount).toBe(2)
  })

  it('a status event gives instant feedback and then resyncs with the server', async () => {
    statusCounts.mockResolvedValue(reply(1))
    const store = useContactsStore()
    store.newCount = 3
    store.applyStatusChange({ contact_id: 'x', old_status: 'new', new_status: 'in_progress' })
    expect(store.newCount).toBe(2) // local delta
    await vi.advanceTimersByTimeAsync(600)
    expect(store.newCount).toBe(1) // server wins (e.g. a scope change happened meanwhile)
  })

  it('a failed refresh keeps the last known value', async () => {
    statusCounts.mockRejectedValue(new Error('offline'))
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const store = useContactsStore()
    store.newCount = 3
    await store.fetchStatusCounts()
    expect(store.newCount).toBe(3)
    spy.mockRestore()
  })
})
