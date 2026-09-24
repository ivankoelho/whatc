import type { OccurrenceDocumentInput, OccurrenceDocumentItem, OccurrenceDocumentType } from '@/services/api'

// A purchase document (NF or cupom/pedido) being typed, plus the file waiting
// to be uploaded once the document exists on the server.
export interface DocumentDraft {
  key: number
  type: OccurrenceDocumentType
  number: string
  purchase_date: string
  items: OccurrenceDocumentItem[]
  file: File | null
}

let nextKey = 0

export function emptyDocumentItem(): OccurrenceDocumentItem {
  return { code: '', description: '', quantity: '' }
}

export function newDocumentDraft(): DocumentDraft {
  return { key: nextKey++, type: 'nf', number: '', purchase_date: '', items: [emptyDocumentItem()], file: null }
}

// A draft the agent left untouched is ignored rather than rejected.
export function isBlankDraft(d: DocumentDraft): boolean {
  return !d.number.trim() && !d.purchase_date && !d.file && d.items.every(i => !i.description.trim() && !i.code.trim())
}

export function draftToInput(d: DocumentDraft): OccurrenceDocumentInput {
  return {
    type: d.type,
    number: d.number.trim(),
    purchase_date: d.purchase_date || undefined,
    items: d.items.filter(i => i.description.trim()),
  }
}

// The values the process "required fields" rules check, derived from the
// documents the same way the backend fills the protocol's summary fields.
export function documentsSummary(docs: DocumentDraft[]) {
  return {
    invoice_number: docs.map(d => d.number.trim()).filter(Boolean).join(', '),
    purchase_date: docs.map(d => d.purchase_date).filter(Boolean).sort()[0] || '',
    product_description: docs.flatMap(d => d.items.map(i => i.description.trim())).filter(Boolean).join('; '),
  }
}

export const MAX_ATTACHMENT_BYTES = 10 * 1024 * 1024
export const ATTACHMENT_ACCEPT = 'application/pdf,image/jpeg,image/png,image/webp'
