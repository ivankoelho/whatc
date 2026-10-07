import { describe, expect, it } from 'vitest'
import { ALL, activeFilters, dayOf, formatMoney, isoDay, lastDays } from './whatsapp-usage'

describe('formatMoney', () => {
  it('keeps the currency and up to 4 decimals for per-message prices', () => {
    expect(formatMoney(0.0068, 'BRL')).toContain('0,0068')
    expect(formatMoney(0.0068, 'BRL')).toContain('R$')
    expect(formatMoney(1.5, 'USD', 'en-US')).toBe('$1.50')
  })
  it('never invents a currency symbol for an unknown code', () => {
    expect(formatMoney(2, 'XXZ')).toBeTruthy()
    expect(formatMoney(2, '')).toBe('2,00')
  })
  it('does not add different currencies: each amount is formatted on its own', () => {
    expect(formatMoney(1, 'BRL')).not.toEqual(formatMoney(1, 'USD'))
  })
})

describe('dates', () => {
  it('lastDays is inclusive of today', () => {
    expect(lastDays(30, new Date(2026, 9, 7))).toEqual({ from: '2026-09-08', to: '2026-10-07' })
    expect(lastDays(1, new Date(2026, 9, 7))).toEqual({ from: '2026-10-07', to: '2026-10-07' })
  })
  it('isoDay uses the local calendar day', () => {
    expect(isoDay(new Date(2026, 0, 5))).toBe('2026-01-05')
  })
  it('dayOf cuts a timestamp to its day', () => {
    expect(dayOf('2026-01-01T00:00:00Z')).toBe('2026-01-01')
    expect(dayOf(null)).toBe('')
  })
})

describe('activeFilters', () => {
  it('drops the all-sentinel and empty values', () => {
    expect(activeFilters({ account: ALL, unit_id: 'u1', category: '', from: '2026-01-01' })).toEqual({ unit_id: 'u1', from: '2026-01-01' })
  })
})
