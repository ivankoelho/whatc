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
    statusCounts.mockReset().mockResolvedValue(reply(4))
    setActivePinia(createPinia())
  })
  afterEach(() => {
    useContactsStore().deactivateStatusCounts() // stop the poll between tests
    vi.useRealTimers()
  })

  describe('chat screen lifecycle', () => {
    it('does nothing while the chat screen is not active', async () => {
      const store = useContactsStore()
      store.scheduleStatusCountsRefresh()
      store.refreshStatusCountsIfActive()
      await vi.advanceTimersByTimeAsync(120_000)
      expect(statusCounts).not.toHaveBeenCalled()
    })

    it('syncs immediately when the chat screen mounts, then polls every 60s', async () => {
      const store = useContactsStore()
      store.activateStatusCounts()
      await vi.advanceTimersByTimeAsync(0)
      expect(statusCounts).toHaveBeenCalledTimes(1)
      expect(store.newCount).toBe(4)

      await vi.advanceTimersByTimeAsync(59_000)
      expect(statusCounts).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(1_000)
      expect(statusCounts).toHaveBeenCalledTimes(2)
      await vi.advanceTimersByTimeAsync(60_000)
      expect(statusCounts).toHaveBeenCalledTimes(3)
    })

    it('leaving the chat stops the poll and drops queued refreshes', async () => {
      const store = useContactsStore()
      store.activateStatusCounts()
      await vi.advanceTimersByTimeAsync(0)
      statusCounts.mockClear()

      store.scheduleStatusCountsRefresh() // queued, not yet fired
      store.deactivateStatusCounts()
      await vi.advanceTimersByTimeAsync(180_000)
      expect(statusCounts).not.toHaveBeenCalled()
    })

    it('a response still in flight when the chat is left does not land', async () => {
      const slow = deferred<ReturnType<typeof reply>>()
      statusCounts.mockReturnValueOnce(slow.promise)
      const store = useContactsStore()
      store.newCount = 1
      store.activateStatusCounts()
      store.deactivateStatusCounts()
      slow.resolve(reply(9))
      await vi.advanceTimersByTimeAsync(0)
      expect(store.newCount).toBe(1)
    })

    it('coming back to the chat syncs again and resumes the poll', async () => {
      const store = useContactsStore()
      store.activateStatusCounts()
      await vi.advanceTimersByTimeAsync(0)
      store.deactivateStatusCounts()
      statusCounts.mockClear()

      store.activateStatusCounts()
      await vi.advanceTimersByTimeAsync(0)
      expect(statusCounts).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(60_000)
      expect(statusCounts).toHaveBeenCalledTimes(2)
    })

    it('activating twice does not stack polls', async () => {
      const store = useContactsStore()
      store.activateStatusCounts()
      store.activateStatusCounts()
      await vi.advanceTimersByTimeAsync(0)
      statusCounts.mockClear()
      await vi.advanceTimersByTimeAsync(60_000)
      expect(statusCounts).toHaveBeenCalledTimes(1)
    })
  })

  describe('WebSocket reconnection', () => {
    it('re-reads the counter right away while the chat is active', async () => {
      const store = useContactsStore()
      store.activateStatusCounts()
      await vi.advanceTimersByTimeAsync(0)
      statusCounts.mockClear()
      store.refreshStatusCountsIfActive()
      await vi.advanceTimersByTimeAsync(0)
      expect(statusCounts).toHaveBeenCalledTimes(1)
    })

    it('does nothing outside the chat (activate syncs on return)', async () => {
      const store = useContactsStore()
      store.refreshStatusCountsIfActive()
      await vi.advanceTimersByTimeAsync(0)
      expect(statusCounts).not.toHaveBeenCalled()
    })
  })

  describe('debounce and maxWait', () => {
    const active = async () => {
      const store = useContactsStore()
      store.activateStatusCounts()
      await vi.advanceTimersByTimeAsync(0)
      statusCounts.mockClear()
      return store
    }

    it('a burst collapses into one request 2s after the last event', async () => {
      const store = await active()
      for (let i = 0; i < 4; i++) {
        store.scheduleStatusCountsRefresh()
        await vi.advanceTimersByTimeAsync(400) // 1.6s of events, each < 2s apart
      }
      expect(statusCounts).not.toHaveBeenCalled()
      await vi.advanceTimersByTimeAsync(1_600)
      expect(statusCounts).toHaveBeenCalledTimes(1)
    })

    it('continuous events cannot starve it: fires 3s after the first one', async () => {
      const store = await active()
      store.scheduleStatusCountsRefresh() // t = 0
      for (let t = 500; t < 3000; t += 500) {
        await vi.advanceTimersByTimeAsync(500) // a new event every 500ms
        store.scheduleStatusCountsRefresh()
      }
      expect(statusCounts).not.toHaveBeenCalled() // t = 2500
      await vi.advanceTimersByTimeAsync(500) // t = 3000
      expect(statusCounts).toHaveBeenCalledTimes(1)
    })

    it('after the ceiling fires, the next burst starts a fresh window', async () => {
      const store = await active()
      store.scheduleStatusCountsRefresh()
      await vi.advanceTimersByTimeAsync(2_000)
      expect(statusCounts).toHaveBeenCalledTimes(1)
      store.scheduleStatusCountsRefresh()
      await vi.advanceTimersByTimeAsync(1_900)
      expect(statusCounts).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(100)
      expect(statusCounts).toHaveBeenCalledTimes(2)
    })
  })

  describe('correctness', () => {
    it('a slow older response never overwrites a newer one', async () => {
      const slow = deferred<ReturnType<typeof reply>>()
      statusCounts.mockReset().mockReturnValueOnce(slow.promise).mockResolvedValueOnce(reply(2))
      const store = useContactsStore()
      const first = store.fetchStatusCounts() // slow, will answer last with a stale 9
      await store.fetchStatusCounts() // newer, answers 2
      expect(store.newCount).toBe(2)
      slow.resolve(reply(9))
      await first
      expect(store.newCount).toBe(2)
    })

    it('a status event gives instant feedback and then resyncs with the server', async () => {
      statusCounts.mockReset().mockResolvedValue(reply(4)).mockResolvedValueOnce(reply(3)) // activation sync
      const store = useContactsStore()
      store.activateStatusCounts()
      await vi.advanceTimersByTimeAsync(0)
      expect(store.newCount).toBe(3)

      statusCounts.mockResolvedValue(reply(1))
      store.applyStatusChange({ contact_id: 'x', old_status: 'new', new_status: 'in_progress' })
      expect(store.newCount).toBe(2) // local delta
      await vi.advanceTimersByTimeAsync(2_100)
      expect(store.newCount).toBe(1) // server wins (e.g. a scope change happened meanwhile)
    })

    it('a failed refresh keeps the last known value', async () => {
      statusCounts.mockReset().mockRejectedValue(new Error('offline'))
      const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
      const store = useContactsStore()
      store.newCount = 3
      await store.fetchStatusCounts()
      expect(store.newCount).toBe(3)
      spy.mockRestore()
    })
  })
})
