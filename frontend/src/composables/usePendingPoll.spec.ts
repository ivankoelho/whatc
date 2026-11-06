// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick, ref } from 'vue'
import { mount } from '@vue/test-utils'
import { usePendingPoll } from './usePendingPoll'

function harness(active: { value: boolean }, tick: () => Promise<unknown>, maxMs?: number) {
  return mount(defineComponent({ setup() { usePendingPoll(() => active.value, tick, maxMs); return () => h('div') } }))
}

describe('usePendingPoll', () => {
  beforeEach(() => { vi.useFakeTimers() })
  afterEach(() => { vi.useRealTimers() })

  it('polls only while something is pending, backs off, and never overlaps itself', async () => {
    const active = ref(true)
    let calls = 0
    let release: () => void = () => {}
    const tick = vi.fn(() => { calls++; return new Promise<void>(r => { release = r }) })
    harness(active, tick)
    await vi.advanceTimersByTimeAsync(4000)
    expect(calls).toBe(1)
    await vi.advanceTimersByTimeAsync(60000) // the first round has not finished: no second one starts
    expect(calls).toBe(1)
    release()
    await vi.advanceTimersByTimeAsync(6000) // next delay is 6 s
    expect(calls).toBe(2)
    active.value = false // released: it stops
    await nextTick()
    release()
    await vi.advanceTimersByTimeAsync(60000)
    expect(calls).toBe(2)
  })

  it('gives up after its limit, and stops when the component goes away', async () => {
    const active = ref(true)
    const tick = vi.fn(async () => {})
    harness(active, tick, 30000)
    await vi.advanceTimersByTimeAsync(120000)
    const n = tick.mock.calls.length
    expect(n).toBeGreaterThan(0)
    expect(n).toBeLessThan(8)
    await vi.advanceTimersByTimeAsync(120000)
    expect(tick.mock.calls.length).toBe(n)

    const w = harness(ref(true), tick)
    w.unmount()
    const before = tick.mock.calls.length
    await vi.advanceTimersByTimeAsync(60000)
    expect(tick.mock.calls.length).toBe(before)
  })
})
