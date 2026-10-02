// Pure helpers of the Knowledge screens (no Vue, no network): the request bodies, the
// validation, the error classes and the small rules that are easy to get wrong.
import type {
  KnowledgeDocument,
  KnowledgeDocumentInput,
  KnowledgeScopeItem,
  KnowledgeSourceType,
  KnowledgeStatus,
} from '@/services/api'

export const KNOWLEDGE_MAX_BODY = 200_000 // characters, the server limit
export const KNOWLEDGE_MAX_TITLE = 500

// manual_html is never created or edited here: its content belongs to the importer (CLI).
export const EDITABLE_SOURCE_TYPES = ['faq', 'article', 'process', 'text', 'markdown'] as const
export type EditableSourceType = (typeof EDITABLE_SOURCE_TYPES)[number]

export interface KnowledgeForm {
  title: string
  body: string
  source_type: EditableSourceType
  unit_id: string | null
  department_id: string | null
  status: KnowledgeStatus
}

export type KnowledgeFormError = 'titleRequired' | 'titleTooLong' | 'bodyRequired' | 'bodyTooLong' | 'sourceInvalid'

// manual_html: title/body/source type are not editable (409 on the server).
export function isManual(doc: Pick<KnowledgeDocument, 'source_type'>): boolean {
  return doc.source_type === 'manual_html'
}

export function validateKnowledgeForm(f: Pick<KnowledgeForm, 'title' | 'body' | 'source_type'>): KnowledgeFormError[] {
  const errors: KnowledgeFormError[] = []
  const title = f.title.trim()
  if (!title) errors.push('titleRequired')
  else if ([...title].length > KNOWLEDGE_MAX_TITLE) errors.push('titleTooLong')
  if (!f.body.trim()) errors.push('bodyRequired')
  else if ([...f.body].length > KNOWLEDGE_MAX_BODY) errors.push('bodyTooLong')
  if (!(EDITABLE_SOURCE_TYPES as readonly string[]).includes(f.source_type)) errors.push('sourceInvalid')
  return errors
}

// Counts characters the way the server does (runes, not UTF-16 units).
export function charCount(text: string): number {
  return [...text].length
}

// POST body. The scope keys are always sent (null = no restriction).
export function buildCreatePayload(f: KnowledgeForm): KnowledgeDocumentInput {
  return {
    title: f.title.trim(),
    body: f.body,
    source_type: f.source_type,
    unit_id: f.unit_id ?? null,
    department_id: f.department_id ?? null,
    status: f.status,
  }
}

// PUT body: the COMPLETE representation. unit_id and department_id are ALWAYS present (UUID or
// null): omitting them would be a 400, and the screen must never rely on "absent = keep".
// A manual_html document sends only scope and status (its content is the importer's).
// expected_updated_at is the version the user read, so a concurrent change answers 409.
export function buildUpdatePayload(
  doc: Pick<KnowledgeDocument, 'source_type'>,
  f: KnowledgeForm,
  expectedUpdatedAt: string,
): KnowledgeDocumentInput {
  const base: KnowledgeDocumentInput = {
    unit_id: f.unit_id ?? null,
    department_id: f.department_id ?? null,
    status: f.status,
    expected_updated_at: expectedUpdatedAt,
  }
  if (isManual(doc)) return base
  return { ...base, title: f.title.trim(), body: f.body, source_type: f.source_type }
}

// Archive / reactivate: a PUT with everything the document already has and only the status changed.
export function formFromDocument(doc: KnowledgeDocument): KnowledgeForm {
  return {
    title: doc.title,
    body: doc.body ?? '',
    source_type: (isManual(doc) ? 'text' : doc.source_type) as EditableSourceType,
    unit_id: doc.unit_id ?? null, // an absent key (the server omits null) is still "no restriction"
    department_id: doc.department_id ?? null,
    status: doc.status,
  }
}

export function emptyKnowledgeForm(): KnowledgeForm {
  return { title: '', body: '', source_type: 'article', unit_id: null, department_id: null, status: 'active' }
}

export type ApiFailure = 'conflict' | 'forbidden' | 'notFound' | 'badRequest' | 'other'

export function classifyError(err: unknown): ApiFailure {
  const status = (err as { response?: { status?: number } } | null)?.response?.status
  switch (status) {
    case 409: return 'conflict'
    case 403: return 'forbidden'
    case 404: return 'notFound'
    case 400: return 'badRequest'
    default: return 'other'
  }
}

export type ScopeKind = 'organization' | 'unit' | 'department' | 'unitDepartment'

export function scopeKind(doc: Pick<KnowledgeDocument, 'unit_id' | 'department_id'>): ScopeKind {
  if (doc.unit_id && doc.department_id) return 'unitDepartment'
  if (doc.unit_id) return 'unit'
  if (doc.department_id) return 'department'
  return 'organization'
}

// The name of a unit/department for display; an id the caller cannot see falls back to a short id.
export function scopeName(items: KnowledgeScopeItem[], id: string | null | undefined): string {
  if (!id) return ''
  const found = items.find(i => i.id === id)
  return found ? found.name : id.slice(0, 8)
}

// Options of a scope picker: inactive units/departments are not offered for a NEW choice,
// but the one a document already has stays in the list (marked), so an old reference is kept.
export function pickerOptions(items: KnowledgeScopeItem[], currentId: string | null): KnowledgeScopeItem[] {
  return items.filter(i => i.active || i.id === currentId)
}

export type WhoArchived = 'none' | 'user' | 'import'

export function whoArchived(doc: Pick<KnowledgeDocument, 'status' | 'archived_by'>): WhoArchived {
  if (doc.status !== 'archived') return 'none'
  return doc.archived_by === 'import' ? 'import' : 'user'
}

// A chunk built by an older indexing strategy. Only who administers knows the current version.
export function isStaleChunk(chunkVersion: number, currentVersion: number | undefined): boolean {
  return currentVersion !== undefined && chunkVersion < currentVersion
}

export function formatScore(score: number): string {
  return score.toFixed(3)
}

export interface KnowledgeCapabilities {
  canRead: boolean
  canWrite: boolean
}

// Write controls exist only with knowledge:write; reading with knowledge:read.
export function knowledgeCapabilities(hasPermission: (resource: string, action: string) => boolean): KnowledgeCapabilities {
  return { canRead: hasPermission('knowledge', 'read'), canWrite: hasPermission('knowledge', 'write') }
}

export type FileReadResult =
  | { ok: true; text: string }
  | { ok: false; reason: 'type' | 'size' }

const TEXT_EXTENSIONS = ['.txt', '.md', '.markdown']

// Upload of text/Markdown: the browser reads the file; nothing is uploaded.
export async function readKnowledgeFile(file: File): Promise<FileReadResult> {
  const name = file.name.toLowerCase()
  const looksText = file.type.startsWith('text/') || TEXT_EXTENSIONS.some(ext => name.endsWith(ext))
  if (!looksText) return { ok: false, reason: 'type' }
  const text = await file.text()
  if (charCount(text) > KNOWLEDGE_MAX_BODY) return { ok: false, reason: 'size' }
  return { ok: true, text }
}

// Source type suggested by the file name (the user can change it).
export function sourceTypeForFile(name: string): EditableSourceType {
  const n = name.toLowerCase()
  return n.endsWith('.md') || n.endsWith('.markdown') ? 'markdown' : 'text'
}

export function isEditableSource(t: KnowledgeSourceType): t is EditableSourceType {
  return (EDITABLE_SOURCE_TYPES as readonly string[]).includes(t)
}

export interface KnowledgeRagSwitch {
  disabled: boolean
  hint: 'serverOff' | 'ready'
}

// The organization's switch for Knowledge in the chatbot's AI replies. It is only usable when the SERVER
// allows it (knowledge_rag_available, read-only); otherwise it is shown disabled with an explanation, so
// nobody sees a working-looking switch while the infrastructure is off.
export function knowledgeRagSwitch(available: boolean | undefined): KnowledgeRagSwitch {
  return available === true ? { disabled: false, hint: 'ready' } : { disabled: true, hint: 'serverOff' }
}
