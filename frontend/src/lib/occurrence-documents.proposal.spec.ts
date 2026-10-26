import { describe, expect, it } from 'vitest'
import { applyProposal, newDocumentDraft, draftToInput } from './occurrence-documents'

const proposal = { number: '987', purchase_date: '2026-10-20', items: [{ code: 'PS-1', description: 'Porcelanato', quantity: '12 M2' }] }

describe('applyProposal', () => {
  it('fills an empty draft', () => {
    const { draft, filled } = applyProposal(newDocumentDraft(), proposal)
    expect(filled).toEqual(['number', 'purchase_date', 'items'])
    expect(draft.number).toBe('987')
    expect(draft.items[0].description).toBe('Porcelanato')
  })

  it('never overwrites what the person already typed', () => {
    const typed = { ...newDocumentDraft(), number: '123', purchase_date: '2026-01-01', items: [{ code: '', description: 'Meu produto', quantity: '' }] }
    const { draft, filled } = applyProposal(typed, proposal)
    expect(filled).toEqual([])
    expect(draft.number).toBe('123')
    expect(draft.purchase_date).toBe('2026-01-01')
    expect(draft.items[0].description).toBe('Meu produto')
  })

  it('records where the values came from only when they were read from a file', () => {
    expect(draftToInput(newDocumentDraft())).not.toHaveProperty('source')
    const read = { ...newDocumentDraft(), source: 'xml' as const, access_key: 'k' }
    expect(draftToInput(read)).toMatchObject({ source: 'xml', access_key: 'k' })
  })
})
