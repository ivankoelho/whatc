// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, ref, nextTick } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'

const status = vi.fn()
vi.mock('@/services/api', () => ({ mediaScanService: { status: (ids: string[]) => status(ids) } }))

import { useMediaScan } from './useMediaScan'

const answer = (statuses: Record<string, string>) => ({ data: { data: { statuses } } })

describe('useMediaScan', () => {
  beforeEach(() => { vi.useFakeTimers(); status.mockReset() })
  afterEach(() => { vi.useRealTimers() })

  it('asks once for the media on screen, shows a pending one as blocked, and releases it by itself', async () => {
    const messages = ref([{ id: 'a', media_url: 'x' }, { id: 'b', media_url: 'y' }, { id: 'c' }])
    status.mockResolvedValueOnce(answer({ a: 'clean', b: 'pending' }))
    let api!: ReturnType<typeof useMediaScan>
    mount(defineComponent({ setup() { api = useMediaScan(messages); return () => h('div') } }))
    await flushPromises()
    expect(status).toHaveBeenCalledTimes(1)
    expect(status).toHaveBeenCalledWith(['a', 'b']) // text messages are not asked about
    expect(api.blockedState('a')).toBeNull()
    expect(api.blockedState('b')).toBe('pending')

    status.mockResolvedValueOnce(answer({ b: 'clean' })) // the poll finds it released
    await vi.advanceTimersByTimeAsync(4000)
    await flushPromises()
    expect(status).toHaveBeenLastCalledWith(['b'])
    expect(api.blockedState('b')).toBeNull()
    expect(api.releasedAt.b).toBeGreaterThan(0) // so the media element reloads with a fresh URL

    // a new media arrives later: only that one is asked about
    status.mockResolvedValueOnce(answer({ d: 'infected' }))
    messages.value = [...messages.value, { id: 'd', media_url: 'z' }]
    await nextTick(); await flushPromises()
    expect(status).toHaveBeenLastCalledWith(['d'])
    expect(api.blockedState('d')).toBe('infected')
    // an infected one is final: no polling for it
    const n = status.mock.calls.length
    await vi.advanceTimersByTimeAsync(60000)
    expect(status.mock.calls.length).toBe(n)
  })
})
