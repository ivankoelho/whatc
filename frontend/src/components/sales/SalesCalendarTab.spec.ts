// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'

const svc = vi.hoisted(() => ({ getCalendarEmbed: vi.fn() }))
vi.mock('@/services/api', () => ({ nextcloudService: svc }))

import SalesCalendarTab from './SalesCalendarTab.vue'

const i18n = createI18n({ legacy: false, locale: 'en', messages: { en } })
const ok = (data: unknown) => Promise.resolve({ data: { data } })
const mountIt = () => mount(SalesCalendarTab, { global: { plugins: [i18n], stubs: { RouterLink: { template: '<a data-testid="to-settings"><slot /></a>' } } } })
const EMBED = 'https://next.example.com/index.php/apps/calendar/embed/Mir5xf9ipMDzY3jN'

describe('CRM Calendar tab', () => {
  beforeEach(() => { svc.getCalendarEmbed.mockReset() })

  it('frames the saved calendar in a sandboxed iframe and offers to open it in a new tab', async () => {
    svc.getCalendarEmbed.mockReturnValue(ok({ url: EMBED, can_configure: false }))
    const w = mountIt()
    await flushPromises()
    const frame = w.find('[data-testid="crm-calendar-frame"]')
    expect(frame.attributes('src')).toBe(EMBED)
    expect(frame.attributes('sandbox')).toContain('allow-scripts')
    expect(frame.attributes('sandbox')).not.toContain('allow-top-navigation')
    expect(frame.attributes('referrerpolicy')).toBe('no-referrer')
    const open = w.find('[data-testid="crm-calendar-open"]')
    expect(open.attributes('href')).toBe(EMBED)
    expect(open.attributes('rel')).toContain('noopener')
  })

  it('tells people to ask an administrator when nothing is set, and an administrator where to set it', async () => {
    svc.getCalendarEmbed.mockReturnValue(ok({ url: '', can_configure: false }))
    let w = mountIt()
    await flushPromises()
    expect(w.find('[data-testid="crm-calendar-frame"]').exists()).toBe(false)
    expect(w.find('[data-testid="crm-calendar-empty"]').text()).toContain('Ask an administrator')
    expect(w.find('[data-testid="to-settings"]').exists()).toBe(false)

    svc.getCalendarEmbed.mockReturnValue(ok({ url: '', can_configure: true }))
    w = mountIt()
    await flushPromises()
    expect(w.find('[data-testid="crm-calendar-empty"]').text()).toContain('embed')
    expect(w.find('[data-testid="to-settings"]').exists()).toBe(true)
  })

  it('says so when the address cannot be loaded', async () => {
    svc.getCalendarEmbed.mockRejectedValue({ response: { status: 500 } })
    const w = mountIt()
    await flushPromises()
    expect(w.find('[data-testid="crm-calendar-failed"]').exists()).toBe(true)
    expect(w.find('[data-testid="crm-calendar-frame"]').exists()).toBe(false)
  })

  it('has the tab strings in both languages', async () => {
    const pt = (await import('@/i18n/locales/pt-BR.json')).default as unknown as { sales: Record<string, string> }
    for (const k of ['tabCalendar', 'calendarEmpty', 'calendarEmptyAdmin', 'calendarConfigure', 'calendarOpen', 'calendarLoadFailed']) {
      expect((en as unknown as { sales: Record<string, string> }).sales[k], `en ${k}`).toBeTruthy()
      expect(pt.sales[k], `pt ${k}`).toBeTruthy()
    }
  })
})
