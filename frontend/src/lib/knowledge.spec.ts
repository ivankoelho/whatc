import { describe, expect, it } from 'vitest'
import type { KnowledgeDocument } from '@/services/api'
import {
  KNOWLEDGE_MAX_BODY,
  buildCreatePayload,
  buildUpdatePayload,
  charCount,
  classifyError,
  emptyKnowledgeForm,
  formFromDocument,
  isStaleChunk,
  knowledgeRagSwitch,
  knowledgeCapabilities,
  pickerOptions,
  readKnowledgeFile,
  scopeKind,
  scopeName,
  sourceTypeForFile,
  validateKnowledgeForm,
  whoArchived,
} from './knowledge'

const doc = (over: Partial<KnowledgeDocument> = {}): KnowledgeDocument => ({
  id: 'd1', organization_id: 'o1', unit_id: null, department_id: null, visibility: 'organization',
  source_type: 'article', title: 'Titulo', body: 'Corpo', status: 'active', archived_by: '',
  created_at: '2026-10-02T10:00:00Z', updated_at: '2026-10-02T10:00:00Z', ...over,
})

describe('request bodies', () => {
  const form = { ...emptyKnowledgeForm(), title: '  Titulo  ', body: 'Corpo' }

  it('POST always carries both scope keys (null = no restriction)', () => {
    const p = buildCreatePayload(form)
    expect(p).toHaveProperty('unit_id', null)
    expect(p).toHaveProperty('department_id', null)
    expect(p.title).toBe('Titulo')
  })

  it('PUT is the complete representation: both scope keys are ALWAYS present', () => {
    const p = buildUpdatePayload(doc(), form, '2026-10-02T10:00:00Z')
    expect(Object.keys(p)).toEqual(expect.arrayContaining(['unit_id', 'department_id', 'title', 'body', 'source_type', 'status', 'expected_updated_at']))
    expect(p.unit_id).toBeNull()
    expect(p.department_id).toBeNull()
    const scoped = buildUpdatePayload(doc(), { ...form, unit_id: 'u1' }, 'x')
    expect(scoped.unit_id).toBe('u1')
    expect(scoped.department_id).toBeNull()
    expect(scoped).toHaveProperty('department_id')
  })

  it('PUT carries the version the user read', () => {
    expect(buildUpdatePayload(doc(), form, 'STAMP').expected_updated_at).toBe('STAMP')
  })

  it('manual_html sends only scope and status (its content belongs to the importer)', () => {
    const p = buildUpdatePayload(doc({ source_type: 'manual_html' }), { ...form, unit_id: 'u1', status: 'archived' }, 'S')
    expect(p).toEqual({ unit_id: 'u1', department_id: null, status: 'archived', expected_updated_at: 'S' })
    expect(p).not.toHaveProperty('title')
    expect(p).not.toHaveProperty('body')
    expect(p).not.toHaveProperty('source_type')
  })

  it('archiving is the document form with only the status changed', () => {
    const d = doc({ unit_id: 'u1', department_id: 'p1', title: 'T', body: 'B', source_type: 'faq' })
    const p = buildUpdatePayload(d, { ...formFromDocument(d), status: 'archived' }, 'S')
    expect(p).toMatchObject({ title: 'T', body: 'B', source_type: 'faq', unit_id: 'u1', department_id: 'p1', status: 'archived' })
  })
})

describe('validation', () => {
  it('requires a title and a body', () => {
    expect(validateKnowledgeForm({ title: ' ', body: '', source_type: 'faq' })).toEqual(['titleRequired', 'bodyRequired'])
    expect(validateKnowledgeForm({ title: 'a', body: 'b', source_type: 'faq' })).toEqual([])
  })

  it('limits the body to 200000 characters, counted like the server (runes)', () => {
    expect(validateKnowledgeForm({ title: 'a', body: 'x'.repeat(KNOWLEDGE_MAX_BODY), source_type: 'text' })).toEqual([])
    expect(validateKnowledgeForm({ title: 'a', body: 'x'.repeat(KNOWLEDGE_MAX_BODY + 1), source_type: 'text' })).toEqual(['bodyTooLong'])
    // an emoji is one character for the server, two UTF-16 units in JavaScript
    expect(charCount('😀😀')).toBe(2)
    expect(validateKnowledgeForm({ title: 'a', body: '😀'.repeat(KNOWLEDGE_MAX_BODY), source_type: 'text' })).toEqual([])
  })

  it('limits the title to 500 characters and refuses manual_html as a type', () => {
    expect(validateKnowledgeForm({ title: 'x'.repeat(501), body: 'b', source_type: 'text' })).toEqual(['titleTooLong'])
    expect(validateKnowledgeForm({ title: 'a', body: 'b', source_type: 'manual_html' as never })).toEqual(['sourceInvalid'])
  })
})

describe('errors', () => {
  it('classifies the statuses the screens handle', () => {
    const e = (status: number) => ({ response: { status } })
    expect(classifyError(e(409))).toBe('conflict')
    expect(classifyError(e(403))).toBe('forbidden')
    expect(classifyError(e(404))).toBe('notFound')
    expect(classifyError(e(400))).toBe('badRequest')
    expect(classifyError(e(500))).toBe('other')
    expect(classifyError(new Error('network'))).toBe('other')
    expect(classifyError(null)).toBe('other')
  })
})

describe('scope display', () => {
  const units = [{ id: 'u1', name: 'PORTO SEGURO', active: true }, { id: 'u2', name: 'ANTIGA', active: false }]

  it('describes the scope of a document', () => {
    expect(scopeKind({ unit_id: null, department_id: null })).toBe('organization')
    expect(scopeKind({ unit_id: 'u1', department_id: null })).toBe('unit')
    expect(scopeKind({ unit_id: null, department_id: 'p1' })).toBe('department')
    expect(scopeKind({ unit_id: 'u1', department_id: 'p1' })).toBe('unitDepartment')
  })

  it('names a unit, falling back to a short id for one the user cannot see', () => {
    expect(scopeName(units, 'u1')).toBe('PORTO SEGURO')
    expect(scopeName(units, '1234567890abcdef')).toBe('12345678')
    expect(scopeName(units, null)).toBe('')
  })

  it('does not offer inactive units for a new choice, but keeps the one a document already has', () => {
    expect(pickerOptions(units, null).map(u => u.id)).toEqual(['u1'])
    expect(pickerOptions(units, 'u2').map(u => u.id)).toEqual(['u1', 'u2'])
  })
})

describe('archiving and index', () => {
  it('says who archived', () => {
    expect(whoArchived({ status: 'active', archived_by: '' })).toBe('none')
    expect(whoArchived({ status: 'archived', archived_by: 'import' })).toBe('import')
    expect(whoArchived({ status: 'archived', archived_by: 'user' })).toBe('user')
    expect(whoArchived({ status: 'archived', archived_by: '' })).toBe('user')
  })

  it('marks a chunk obsolete only when the current version is known', () => {
    expect(isStaleChunk(1, 2)).toBe(true)
    expect(isStaleChunk(2, 2)).toBe(false)
    expect(isStaleChunk(1, undefined)).toBe(false)
  })
})

describe('permissions', () => {
  it('write controls need knowledge:write, reading needs knowledge:read', () => {
    const only = (granted: string[]) => (r: string, a: string) => granted.includes(`${r}:${a}`)
    expect(knowledgeCapabilities(only(['knowledge:read']))).toEqual({ canRead: true, canWrite: false })
    expect(knowledgeCapabilities(only(['knowledge:read', 'knowledge:write']))).toEqual({ canRead: true, canWrite: true })
    expect(knowledgeCapabilities(only(['conversations:view_all']))).toEqual({ canRead: false, canWrite: false })
  })
})

describe('file upload', () => {
  it('reads text and Markdown, refuses other types and oversized files', async () => {
    const md = new File(['# Titulo\ntexto'], 'processo.md', { type: '' })
    expect(await readKnowledgeFile(md)).toEqual({ ok: true, text: '# Titulo\ntexto' })
    expect(await readKnowledgeFile(new File(['x'], 'a.txt', { type: 'text/plain' }))).toEqual({ ok: true, text: 'x' })
    expect(await readKnowledgeFile(new File(['%PDF'], 'a.pdf', { type: 'application/pdf' }))).toEqual({ ok: false, reason: 'type' })
    const big = new File(['x'.repeat(KNOWLEDGE_MAX_BODY + 1)], 'grande.txt', { type: 'text/plain' })
    expect(await readKnowledgeFile(big)).toEqual({ ok: false, reason: 'size' })
  })

  it('suggests Markdown for .md files', () => {
    expect(sourceTypeForFile('Processo.MD')).toBe('markdown')
    expect(sourceTypeForFile('notas.txt')).toBe('text')
  })
})

describe('documents whose null scope the server omits from the JSON', () => {
  // The API leaves unit_id/department_id OUT when they are null (omitempty): the keys are absent.
  const raw = { id: 'd1', organization_id: 'o1', visibility: 'organization', source_type: 'article', title: 'T', body: 'B',
    status: 'active', archived_by: '', created_at: 'x', updated_at: 'x' } as unknown as KnowledgeDocument

  it('PUT still carries BOTH scope keys, as explicit null', () => {
    const form = formFromDocument(raw)
    expect(form.unit_id).toBeNull()
    expect(form.department_id).toBeNull()
    const body = JSON.parse(JSON.stringify(buildUpdatePayload(raw, form, 'S'))) // what really goes on the wire
    expect(body).toHaveProperty('unit_id', null)
    expect(body).toHaveProperty('department_id', null)
  })

  it('archiving such a document sends the complete representation with explicit nulls', () => {
    const body = JSON.parse(JSON.stringify(buildUpdatePayload(raw, { ...formFromDocument(raw), status: 'archived' }, 'S')))
    expect(body).toMatchObject({ unit_id: null, department_id: null, status: 'archived', title: 'T', body: 'B' })
  })

  it('manual_html with absent scope keys also sends explicit nulls', () => {
    const manual = { ...raw, source_type: 'manual_html' } as KnowledgeDocument
    const body = JSON.parse(JSON.stringify(buildUpdatePayload(manual, formFromDocument(manual), 'S')))
    expect(body).toEqual({ unit_id: null, department_id: null, status: 'active', expected_updated_at: 'S' })
  })

  it('a form built with undefined scope (defensive) still serializes explicit nulls', () => {
    const f = { ...emptyKnowledgeForm(), unit_id: undefined as unknown as null, department_id: undefined as unknown as null, title: 't', body: 'b' }
    expect(JSON.parse(JSON.stringify(buildCreatePayload(f)))).toMatchObject({ unit_id: null, department_id: null })
  })

  it('describes the scope of such a document as the whole organization', () => {
    expect(scopeKind(raw)).toBe('organization')
  })
})

describe('chatbot Knowledge switch', () => {
  it('is usable only when the server allows Knowledge', () => {
    expect(knowledgeRagSwitch(true)).toEqual({ disabled: false, hint: 'ready' })
    expect(knowledgeRagSwitch(false)).toEqual({ disabled: true, hint: 'serverOff' })
    expect(knowledgeRagSwitch(undefined)).toEqual({ disabled: true, hint: 'serverOff' })
  })
})
