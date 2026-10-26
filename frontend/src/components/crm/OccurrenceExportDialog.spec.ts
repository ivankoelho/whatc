// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'

const svc = vi.hoisted(() => ({ options: vi.fn(), run: vi.fn() }))
vi.mock('@/services/api', () => ({ occurrenceExportService: svc }))
vi.mock('vue-sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import OccurrenceExportDialog from './OccurrenceExportDialog.vue'

const i18n = createI18n({ legacy: false, locale: 'en', messages: { en } })
const sections = (extra: Record<string, boolean> = {}) =>
  Promise.resolve({ data: { data: { sections: { summary: true, history: true, documents: true, internal_chat: true, whatsapp: false, ...extra } } } })

async function open() {
  mount(OccurrenceExportDialog, { props: { open: true, occurrenceId: 'o1' }, global: { plugins: [i18n] }, attachTo: document.body })
  await flushPromises()
}
const q = (id: string) => document.querySelector(`[data-testid="${id}"]`) as HTMLInputElement | HTMLElement

describe('OccurrenceExportDialog', () => {
  beforeEach(() => { svc.options.mockReset().mockReturnValue(sections()); svc.run.mockReset(); document.body.innerHTML = '' })

  it('shows a section the person may not export as disabled and never selects it', async () => {
    await open()
    expect((q('export-section-whatsapp') as HTMLInputElement).disabled).toBe(true)
    ;(q('export-all') as HTMLElement).click()
    await flushPromises()
    expect((q('export-section-whatsapp') as HTMLInputElement).checked).toBe(false)
    expect((q('export-section-history') as HTMLInputElement).checked).toBe(true)
  })

  it('CSV needs exactly one table: the run button waits for it', async () => {
    await open()
    ;(q('export-format-csv') as HTMLElement).click()
    await flushPromises()
    expect((q('export-run') as HTMLButtonElement).disabled).toBe(true) // summary + history selected
    ;(q('export-section-summary') as HTMLElement).click()
    await flushPromises()
    expect((q('export-run') as HTMLButtonElement).disabled).toBe(false)
  })

  it('asks the server for exactly the chosen sections and format', async () => {
    svc.run.mockResolvedValue({ data: new Blob(['x']), headers: { 'content-disposition': 'attachment; filename="p.pdf"' } })
    URL.createObjectURL = vi.fn(() => 'blob:x')
    await open()
    ;(q('export-run') as HTMLElement).click()
    await flushPromises()
    expect(svc.run).toHaveBeenCalledWith('o1', ['summary', 'history'], 'pdf')
  })
})
