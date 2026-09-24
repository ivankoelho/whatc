import { describe, it, expect } from 'vitest'
import { newDocumentDraft, isBlankDraft, documentsSummary, draftToInput } from './occurrence-documents'

describe('occurrence documents', () => {
  it('treats an untouched draft as blank, anything typed as filled', () => {
    const d = newDocumentDraft()
    expect(isBlankDraft(d)).toBe(true)
    d.items[0].description = 'Piso'
    expect(isBlankDraft(d)).toBe(false)
  })

  it('summarizes documents the way the backend fills the protocol fields', () => {
    const nf = { ...newDocumentDraft(), number: '123', purchase_date: '2026-09-10', items: [{ code: '', description: 'Piso', quantity: '' }] }
    const cupom = { ...newDocumentDraft(), type: 'cupom' as const, number: 'PED-7', purchase_date: '2026-09-02', items: [{ code: '', description: ' ', quantity: '' }] }
    expect(documentsSummary([nf, cupom])).toEqual({
      invoice_number: '123, PED-7',
      purchase_date: '2026-09-02',
      product_description: 'Piso',
    })
  })

  it('drops product lines without a description before sending', () => {
    const d = { ...newDocumentDraft(), number: ' 9 ', items: [{ code: 'A', description: '', quantity: '1' }, { code: '', description: 'Rodapé', quantity: '2 un' }] }
    expect(draftToInput(d)).toEqual({ type: 'nf', number: '9', purchase_date: undefined, items: [{ code: '', description: 'Rodapé', quantity: '2 un' }] })
  })
})
