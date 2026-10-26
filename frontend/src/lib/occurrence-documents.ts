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
  // The XML the values were read from: the attachment slot takes PDFs and photos only, so the XML is
  // kept as a general case file when the document is saved (the original is never thrown away).
  original?: File | null
  // Filled when the values were read from the file; cleared if the file is removed.
  source?: 'xml' | 'pdf_text'
  access_key?: string
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
    ...(d.source ? { source: d.source, access_key: d.access_key } : {}),
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

export interface Proposal {
  number?: string
  purchase_date?: string
  items: OccurrenceDocumentItem[]
}

/**
 * Applies what was read from an invoice to a draft WITHOUT overwriting anything the person already
 * typed: a field is filled only when it is empty. Returns the draft and the names of the fields it filled.
 */
export function applyProposal(d: DocumentDraft, p: Proposal): { draft: DocumentDraft; filled: string[] } {
  const filled: string[] = []
  const draft: DocumentDraft = { ...d, items: d.items.map(i => ({ ...i })) }
  if (p.number && !draft.number.trim()) { draft.number = p.number; filled.push('number') }
  if (p.purchase_date && !draft.purchase_date) { draft.purchase_date = p.purchase_date; filled.push('purchase_date') }
  const blank = draft.items.every(i => !i.description.trim() && !i.code.trim())
  if (blank && p.items.length > 0) { draft.items = p.items.map(i => ({ ...i })); filled.push('items') }
  return { draft, filled }
}
