import { describe, it, expect } from 'vitest'
import {
  buildCsvRecipients,
  buildManualRecipients,
  localInputToUTC,
  manualValidUntilIndex,
  parseValidUntil,
  type RecipientContext,
} from './campaignRecipients'

const NOW = new Date('2030-01-01T12:00:00Z')
const auth: RecipientContext = { headerName: null, bodyNames: ['1'], isAuth: true }
const normal: RecipientContext = { headerName: null, bodyNames: ['name', 'order'], isAuth: false }

describe('parseValidUntil', () => {
  it('converts any offset to the same UTC instant', () => {
    const z = parseValidUntil('2030-01-01T13:00:00Z', NOW)
    const brt = parseValidUntil('2030-01-01T10:00:00-03:00', NOW)
    const jst = parseValidUntil('2030-01-01T22:00:00+09:00', NOW)
    const compact = parseValidUntil('2030-01-01T10:00:00-0300', NOW)
    const spaced = parseValidUntil('2030-01-01 10:00:00-03:00', NOW)
    for (const r of [z, brt, jst, compact, spaced]) expect(r.iso).toBe('2030-01-01T13:00:00.000Z')
  })

  it('accepts a missing seconds part and fractions', () => {
    expect(parseValidUntil('2030-01-01T13:00Z', NOW).iso).toBe('2030-01-01T13:00:00.000Z')
    expect(parseValidUntil('2030-01-01T13:00:00.500Z', NOW).iso).toBe('2030-01-01T13:00:00.500Z')
  })

  it('refuses values without a time zone: the instant would be ambiguous', () => {
    expect(parseValidUntil('2030-01-01T13:00:00', NOW).error).toMatch(/time zone/)
    expect(parseValidUntil('2030-01-01', NOW).error).toBeDefined()
    expect(parseValidUntil('tomorrow', NOW).error).toBeDefined()
  })

  it('refuses impossible dates and moments already in the past', () => {
    expect(parseValidUntil('2030-13-45T10:00:00Z', NOW).error).toBeDefined()
    expect(parseValidUntil('2030-01-01T11:59:59Z', NOW).error).toMatch(/past/)
    expect(parseValidUntil('2030-01-01T12:00:00Z', NOW).error).toMatch(/past/)
  })
})

describe('localInputToUTC', () => {
  it('turns the datetime-local value of the user into UTC and tolerates empty/invalid input', () => {
    expect(localInputToUTC('')).toBeNull()
    expect(localInputToUTC('not a date')).toBeNull()
    const expected = new Date('2030-06-01T09:30').toISOString() // interpreted in the browser's zone
    expect(localInputToUTC('2030-06-01T09:30')).toBe(expected)
  })
})

describe('manual entry', () => {
  it('reads valid_until per recipient from the trailing column (authentication)', () => {
    const lines = [
      '+5511999990001, Ana, 123456, 2030-01-01T13:00:00Z',
      '+5511999990002, Bia, 654321, 2030-01-01T10:05:00-03:00',
    ]
    const out = buildManualRecipients(lines, auth)
    expect(out[0]).toEqual({
      phone_number: '+5511999990001',
      recipient_name: 'Ana',
      template_params: { '1': '123456' },
      valid_until: '2030-01-01T13:00:00.000Z',
    })
    expect(out[1].valid_until).toBe('2030-01-01T13:05:00.000Z')
    expect(manualValidUntilIndex(auth)).toBe(3)
  })

  it('uses the validity the user typed for the batch only where the line has none', () => {
    const ctx = { ...auth, defaultValidUntil: '2030-01-01T14:00:00.000Z' }
    const out = buildManualRecipients(['+5511999990001, Ana, 123456', '+5511999990002, Bia, 654321, 2030-01-01T13:00:00Z'], ctx)
    expect(out[0].valid_until).toBe('2030-01-01T14:00:00.000Z')
    expect(out[1].valid_until).toBe('2030-01-01T13:00:00.000Z')
  })

  it('never invents a validity: nothing informed, nothing sent', () => {
    const out = buildManualRecipients(['+5511999990001, Ana, 123456'], auth)
    expect(out[0]).not.toHaveProperty('valid_until')
  })

  it('header value shifts the validity column, normal templates are untouched', () => {
    const withHeader: RecipientContext = { headerName: 'title', bodyNames: ['1'], isAuth: true }
    const out = buildManualRecipients(['+5511999990001, Ana, Mr, 123456, 2030-01-01T13:00:00Z'], withHeader)
    expect(out[0].header_params).toEqual({ title: 'Mr' })
    expect(out[0].template_params).toEqual({ '1': '123456' })
    expect(out[0].valid_until).toBe('2030-01-01T13:00:00.000Z')

    const n = buildManualRecipients(['+5511999990001, Ana, John, ORD-1'], { ...normal, defaultValidUntil: '2030-01-01T14:00:00.000Z' })
    expect(n[0]).toEqual({ phone_number: '+5511999990001', recipient_name: 'Ana', template_params: { name: 'John', order: 'ORD-1' } })
  })
})

describe('CSV import', () => {
  it('maps a valid_until column per recipient and keeps it out of the body parameters', () => {
    const csv = 'phone_number,name,1,valid_until\n+5511999990001,Ana,123456,2030-01-01T13:00:00Z\n+5511999990002,Bia,654321,2030-01-01T10:05:00-03:00\n'
    const r = buildCsvRecipients(csv, auth, NOW)
    expect(r.error).toBeUndefined()
    expect(r.recipients[0]).toEqual({
      phone_number: '+5511999990001', recipient_name: 'Ana', template_params: { '1': '123456' }, valid_until: '2030-01-01T13:00:00.000Z',
    })
    expect(r.recipients[1].valid_until).toBe('2030-01-01T13:05:00.000Z')
  })

  it('reserves the valid_until column even when parameters are matched by position', () => {
    // Columns have no parameter names: the first free column is the code, never valid_until.
    const csv = 'phone,valid_until,code\n+5511999990001,2030-01-01T13:00:00Z,123456\n'
    const r = buildCsvRecipients(csv, auth, NOW)
    expect(r.recipients[0].template_params).toEqual({ '1': '123456' })
    expect(r.recipients[0].valid_until).toBe('2030-01-01T13:00:00.000Z')
  })

  it('applies the batch validity the user typed only to rows without their own, and sends nothing otherwise', () => {
    const csv = 'phone_number,1,valid_until\n+5511999990001,111111,\n+5511999990002,222222,2030-01-01T13:00:00Z\n'
    const withDefault = buildCsvRecipients(csv, { ...auth, defaultValidUntil: '2030-01-01T14:00:00.000Z' }, NOW)
    expect(withDefault.recipients.map((r) => r.valid_until)).toEqual(['2030-01-01T14:00:00.000Z', '2030-01-01T13:00:00.000Z'])

    const without = buildCsvRecipients(csv, auth, NOW)
    expect(without.recipients[0]).not.toHaveProperty('valid_until')
    expect(without.recipients[1].valid_until).toBe('2030-01-01T13:00:00.000Z')
  })

  it('an existing authentication CSV without the column imports exactly as before', () => {
    const r = buildCsvRecipients('phone_number,name,1\n+5511999990001,Ana,123456\n', auth, NOW)
    expect(r.recipients).toEqual([{ phone_number: '+5511999990001', recipient_name: 'Ana', template_params: { '1': '123456' } }])
  })

  it('stops on a bad validity instead of silently dropping or guessing it', () => {
    const csv = 'phone_number,1,valid_until\n+5511999990001,111111,2030-01-01T13:00:00Z\n+5511999990002,222222,01/01/2030 10:00\n+5511999990003,333333,2020-01-01T10:00:00Z\n'
    const r = buildCsvRecipients(csv, auth, NOW)
    expect(r.error).toBe('invalid_rows')
    expect(r.recipients).toEqual([])
    expect(r.rowErrors.map((e) => e.line)).toEqual([3, 4])
  })

  it('normal templates: a valid_until column is just another column and no validity is ever added', () => {
    const csv = 'phone_number,name,order,valid_until\n+5511999990001,Ana,John,ORD-1,2030-01-01T13:00:00Z\n'
    const r = buildCsvRecipients(csv, { ...normal, defaultValidUntil: '2030-01-01T14:00:00.000Z' }, NOW)
    expect(r.recipients[0]).not.toHaveProperty('valid_until')
    // behaviour as before: "order" matched by name, "name" param falls back by position
    expect(r.recipients[0].template_params).toEqual({ order: 'John', name: 'ORD-1' })
  })

  it('reports the same errors as before for empty files and missing phone column', () => {
    expect(buildCsvRecipients('phone_number\n', normal).error).toBe('empty')
    expect(buildCsvRecipients('name,order\nAna,1\n', normal).error).toBe('missing_phone')
    expect(buildCsvRecipients('phone_number,name\n,Ana\n', { headerName: null, bodyNames: [], isAuth: false }).error).toBe('no_valid_rows')
  })
})
