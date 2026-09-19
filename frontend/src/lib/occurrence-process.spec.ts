import { describe, it, expect } from 'vitest'
import { isProcessFieldRequired, missingProcessFields, unresolvedPlaceholders } from './occurrence-process'
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

describe('unresolvedPlaceholders', () => {
  it('returns [] for an empty string', () => {
    expect(unresolvedPlaceholders('')).toEqual([])
  })
  it('returns [] when no variable is left', () => {
    expect(unresolvedPlaceholders('Seu protocolo é 202600123.')).toEqual([])
  })
  it('returns a single leftover token', () => {
    expect(unresolvedPlaceholders('NF [NF] registrada')).toEqual(['[NF]'])
  })
  it('returns several distinct tokens in order of appearance', () => {
    expect(unresolvedPlaceholders('[Nome], prazo [Prazo], loja [Loja]')).toEqual(['[Nome]', '[Prazo]', '[Loja]'])
  })
  it('counts a repeated token once', () => {
    expect(unresolvedPlaceholders('[NF] e de novo [NF]')).toEqual(['[NF]'])
  })
  it('ignores bracketed text that is not a known variable', () => {
    expect(unresolvedPlaceholders('Veja [anexo] e [Nota] e [nf]')).toEqual([])
  })
})
