import { describe, it, expect } from 'vitest'
import { isProcessFieldRequired, missingProcessFields } from './occurrence-process'
import type { OccurrenceProcess } from '@/services/api'

const process: OccurrenceProcess = {
  id: '1', name: 'Avaria', description: '', guidance: '', restrictions: '',
  evidence_checklist: [], required_fields: ['invoice_number', 'product_description'],
  is_active: true, position: 0,
}

describe('isProcessFieldRequired', () => {
  it('is true for a listed key', () => {
    expect(isProcessFieldRequired(process, 'invoice_number')).toBe(true)
  })
  it('is false for an unlisted key', () => {
    expect(isProcessFieldRequired(process, 'sale_channel')).toBe(false)
  })
  it('is false with no process', () => {
    expect(isProcessFieldRequired(null, 'invoice_number')).toBe(false)
  })
})

describe('missingProcessFields', () => {
  it('returns required keys whose value is blank', () => {
    expect(missingProcessFields(process, { invoice_number: '  ', product_description: 'Piso' }))
      .toEqual(['invoice_number'])
  })
  it('returns [] when nothing is required', () => {
    expect(missingProcessFields(null, {})).toEqual([])
  })
  it('treats an absent key as blank', () => {
    expect(missingProcessFields(process, {})).toEqual(['invoice_number', 'product_description'])
  })
})
