// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest'
import { formatCivilDate, formatDate, formatDateTime, formatDateTimeSeconds, formatTime, setDisplayTimeZone, getDisplayTimeZone } from './datetime'

const lang = (l: string) => document.documentElement.setAttribute('lang', l)
// 26 Oct 2026 13:05:09 UTC: 10:05 in São Paulo (UTC-3), 09:05 in Manaus (UTC-4), 22:05 in Tokyo (same day), 02:05 next day in Auckland
const iso = '2026-10-26T13:05:09Z'

describe('datetime', () => {
  afterEach(() => { setDisplayTimeZone(undefined); lang('') })

  it('pt-BR shows DD/MM/YYYY and HH:mm in 24 hours, in the organization zone', () => {
    lang('pt-BR')
    setDisplayTimeZone('America/Sao_Paulo')
    expect(formatDate(iso)).toBe('26/10/2026')
    expect(formatTime(iso)).toBe('10:05')
    expect(formatDateTime(iso)).toBe('26/10/2026 10:05')
    expect(formatDateTimeSeconds(iso)).toBe('26/10/2026 10:05:09')
    setDisplayTimeZone('America/Manaus')
    expect(formatDateTime(iso)).toBe('26/10/2026 09:05')
  })

  it('never shows 24:xx at midnight and has no 12-hour marker', () => {
    lang('pt-BR')
    setDisplayTimeZone('America/Sao_Paulo')
    expect(formatDateTime('2026-10-26T03:00:00Z')).toBe('26/10/2026 00:00')
    expect(formatDateTime('2026-10-26T15:30:00Z')).toBe('26/10/2026 12:30')
    expect(formatDateTime('2026-10-26T21:30:00Z')).toBe('26/10/2026 18:30')
  })

  it('the zone can move the day, which is the point of using the organization zone', () => {
    lang('pt-BR')
    setDisplayTimeZone('Pacific/Auckland')
    expect(formatDate(iso)).toBe('27/10/2026')
  })

  it('an invalid or missing zone falls back to the browser zone instead of throwing', () => {
    lang('pt-BR')
    setDisplayTimeZone('Not/AZone')
    expect(getDisplayTimeZone()).toBe(Intl.DateTimeFormat().resolvedOptions().timeZone)
    expect(formatDateTime(iso)).toMatch(/^\d{2}\/\d{2}\/2026 \d{2}:\d{2}$/)
  })

  it('English keeps its regional format', () => {
    lang('en')
    setDisplayTimeZone('America/Sao_Paulo')
    expect(formatDateTime(iso)).toBe('Oct 26, 2026 10:05 AM')
  })

  it('a pure calendar date is not shifted by the zone', () => {
    lang('pt-BR')
    setDisplayTimeZone('America/Sao_Paulo')
    expect(formatCivilDate('2026-10-20')).toBe('20/10/2026')
    expect(formatCivilDate('2026-10-20T00:00:00Z')).toBe('20/10/2026') // as an instant it would read 19/10 21:00 in São Paulo
    expect(formatDate('2026-10-20T00:00:00Z')).toBe('19/10/2026')
  })

  it('empty or broken values show nothing', () => {
    expect(formatDateTime(undefined)).toBe('')
    expect(formatDateTime('not a date')).toBe('')
    expect(formatCivilDate(null)).toBe('')
  })
})
