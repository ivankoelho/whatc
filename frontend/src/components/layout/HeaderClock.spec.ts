// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import HeaderClock from './HeaderClock.vue'

describe('HeaderClock', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] })
    vi.setSystemTime(new Date('2026-11-20T23:38:16Z')) // 20:38:16 in São Paulo (UTC-3)
  })
  afterEach(() => vi.useRealTimers())

  it('shows São Paulo time as HH:MM (24h, no seconds), the Brazilian date, and follows the minute', async () => {
    const w = mount(HeaderClock)
    const clock = w.find('[data-testid="clock"]').text()
    expect(clock).toContain('20:38')
    expect(clock).not.toContain('20:38:16')
    expect(clock).toContain('20/11/2026')
    vi.advanceTimersByTime(60_000)
    await flushPromises()
    expect(w.find('[data-testid="clock"]').text()).toContain('20:39')
  })
})
