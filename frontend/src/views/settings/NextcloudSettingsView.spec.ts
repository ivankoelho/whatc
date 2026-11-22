// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'
import ptBR from '@/i18n/locales/pt-BR.json'

const svc = vi.hoisted(() => ({ status: vi.fn(), saveServer: vi.fn(), getCalendar: vi.fn(), saveCalendar: vi.fn() }))
vi.mock('@/services/api', async (orig) => ({ ...(await orig<object>()), nextcloudService: svc }))
const toast = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn() }))
vi.mock('vue-sonner', () => ({ toast }))

import NextcloudSettingsView from './NextcloudSettingsView.vue'

const i18n = createI18n({ legacy: false, locale: 'en', messages: { en } })
const ok = (data: unknown) => Promise.resolve({ data: { data } })
const mountIt = () => mount(NextcloudSettingsView, {
  global: {
    plugins: [i18n],
    stubs: {
      Checkbox: { props: ['checked'], emits: ['update:checked'], template: '<input type="checkbox" :checked="checked" @change="$emit(\'update:checked\', $event.target.checked)" />' },
    },
  },
})

describe('NextcloudSettingsView calendar', () => {
  beforeEach(() => {
    Object.values(svc).forEach(f => f.mockReset())
    toast.error.mockReset(); toast.success.mockReset()
    svc.status.mockReturnValue(ok({ configured: true, connected: false, base_url: 'https://nc.example.com', can_configure: true }))
    svc.getCalendar.mockReturnValue(ok({ configured: false }))
    svc.saveCalendar.mockResolvedValue({})
  })

  it('is offered only after the Nextcloud address is registered', async () => {
    svc.status.mockReturnValue(ok({ configured: false, connected: false, can_configure: true }))
    const w = mountIt()
    await flushPromises()
    expect(w.find('[data-testid="calendar-card"]').exists()).toBe(false)
  })

  it('saves the booking rules with the weekdays turned into the hours the server reads', async () => {
    const w = mountIt()
    await flushPromises()

    await w.find('[data-testid="calendar-enabled"]').setValue(true)
    await w.find('[data-testid="calendar-path"]').setValue('/remote.php/dav/calendars/triagem/personal/')
    await w.find('[data-testid="calendar-username"]').setValue('triagem')
    await w.find('[data-testid="calendar-password"]').setValue('app-pass')
    await w.find('[data-testid="calendar-slot"]').setValue('45')
    await w.find('[data-testid="calendar-day-6"]').setValue(true) // Saturday too
    await w.find('[data-testid="calendar-form"]').trigger('submit')
    await flushPromises()

    expect(svc.saveCalendar).toHaveBeenCalledTimes(1)
    const body = svc.saveCalendar.mock.calls[0][0]
    expect(body).toMatchObject({ enabled: true, calendar_path: '/remote.php/dav/calendars/triagem/personal/', username: 'triagem', app_password: 'app-pass', slot_minutes: 45 })
    expect(body.hours).toEqual({ 1: '09:00-18:00', 2: '09:00-18:00', 3: '09:00-18:00', 4: '09:00-18:00', 5: '09:00-18:00', 6: '09:00-18:00' })
    expect(toast.success).toHaveBeenCalled()
  })

  it('never sends or shows the stored password: an empty box keeps it', async () => {
    svc.getCalendar.mockReturnValue(ok({
      configured: true, password_set: true,
      calendar: { enabled: true, calendar_path: '/remote.php/dav/calendars/t/p/', username: 't', time_zone: 'America/Sao_Paulo', slot_minutes: 30, buffer_minutes: 0, min_notice_hours: 2, lookahead_days: 14, hours: { 1: '08:00-12:00' }, location: 'Centro' },
    }))
    const w = mountIt()
    await flushPromises()

    expect((w.find('[data-testid="calendar-password"]').element as HTMLInputElement).value).toBe('')
    expect(w.find('[data-testid="calendar-password"]').attributes('placeholder')).toContain('keep')
    expect((w.find('[data-testid="calendar-day-2"]').element as HTMLInputElement).checked).toBe(false) // only Monday was open
    await w.find('[data-testid="calendar-form"]').trigger('submit')
    await flushPromises()
    const body = svc.saveCalendar.mock.calls[0][0]
    expect(body).not.toHaveProperty('app_password')
    expect(body.hours).toEqual({ 1: '08:00-12:00' })
  })

  it('explains a refused save in the interface language', async () => {
    svc.saveCalendar.mockRejectedValue({ response: { data: { error_type: 'nextcloud_auth' } } })
    const w = mountIt()
    await flushPromises()
    await w.find('[data-testid="calendar-form"]').trigger('submit')
    await flushPromises()
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining('did not accept'))
  })

  it('scrolls by itself: the hub gives a tab no scroll, and the Save button is at the bottom of a long card', async () => {
    const w = mountIt()
    await flushPromises()
    const scroll = w.find('[data-testid="nextcloud-settings-scroll"]')
    expect(scroll.classes()).toContain('overflow-y-auto')
    expect(scroll.classes()).toContain('h-full')
    expect(scroll.find('[data-testid="calendar-save"]').exists()).toBe(true) // the button lives inside the scrolling box
  })

  it('has the calendar strings in both languages', () => {
    const keys = (o: Record<string, unknown>): string[] => Object.entries(o).flatMap(([k, v]) => (v && typeof v === 'object' ? keys(v as Record<string, unknown>).map(s => `${k}.${s}`) : [k]))
    const enKeys = keys((en as any).integrations.nextcloud.calendar).sort()
    const ptKeys = keys((ptBR as any).integrations.nextcloud.calendar).sort()
    expect(ptKeys).toEqual(enKeys)
  })
})
