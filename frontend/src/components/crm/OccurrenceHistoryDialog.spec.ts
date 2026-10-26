// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'

const history = vi.hoisted(() => vi.fn())
vi.mock('@/services/api', () => ({ occurrencesService: { history } }))

import OccurrenceHistoryDialog from './OccurrenceHistoryDialog.vue'

const i18n = createI18n({ legacy: false, locale: 'en', messages: { en } })
const ev = (id: string, type: string, content = '', extra: Record<string, unknown> = {}) =>
  ({ id, occurrence_id: 'o1', type, content, metadata: null, created_by_name: 'Ana', created_at: '2026-10-26T10:00:00Z', ...extra })
const page = (events: unknown[], more = false, next = '') => Promise.resolve({ data: { data: { events, has_more: more, next_cursor: next } } })

function open() {
  return mount(OccurrenceHistoryDialog, {
    props: { open: true, occurrenceId: 'o1', protocol: '2026-000222', title: 'Pedido 55501' },
    global: { plugins: [i18n] }, attachTo: document.body
  })
}

describe('OccurrenceHistoryDialog', () => {
  beforeEach(() => { history.mockReset(); document.body.innerHTML = '' })

  it('shows the first page, withholds redacted text and says the history ended', async () => {
    history.mockReturnValue(page([ev('a', 'note', 'Conversei com o cliente'), ev('b', 'reply', '', { redacted: true })]))
    open()
    await flushPromises()
    expect(history).toHaveBeenCalledWith('o1', expect.objectContaining({ limit: 30 }))
    const body = document.body.textContent ?? ''
    expect(body).toContain('Conversei com o cliente')
    expect(body).toContain('not available for your profile')
    expect(body).toContain('End of the history.')
  })

  it('a filter reloads from the start with the chosen types', async () => {
    history.mockReturnValue(page([ev('a', 'note', 'x')]))
    open()
    await flushPromises()
    vi.useFakeTimers()
    try {
      ;(document.querySelector('[data-testid="history-filter-note"]') as HTMLElement).click()
      await vi.advanceTimersByTimeAsync(400)
    } finally {
      vi.useRealTimers()
    }
    await flushPromises()
    expect(history).toHaveBeenLastCalledWith('o1', expect.objectContaining({ types: 'note', cursor: undefined }))
  })

  it('shows an error with a retry, and an empty state', async () => {
    history.mockRejectedValueOnce(new Error('boom')).mockReturnValueOnce(page([]))
    open()
    await flushPromises()
    expect(document.querySelector('[data-testid="history-error"]')).not.toBeNull()
    ;(document.querySelector('[data-testid="history-error"] button') as HTMLElement).click()
    await flushPromises()
    expect(document.querySelector('[data-testid="history-empty"]')).not.toBeNull()
  })
})
