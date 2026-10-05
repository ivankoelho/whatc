import { describe, expect, it } from 'vitest'
import { CONTACT_PANEL_FIELDS, isContactPanelField, resolveContactPanelField, type ContactPanelLookups } from './contact-registration'

const lookups: ContactPanelLookups = {
  typeLabel: (t) => ({ cliente: 'Cliente', fornecedor: 'Fornecedor', colaborador: 'Colaborador' } as Record<string, string>)[t] ?? t,
  unitName: (id) => ({ u1: 'Feira 2' } as Record<string, string>)[id],
  departmentName: (id) => ({ d1: 'Expedição' } as Record<string, string>)[id],
  defined: 'Definido',
}
const resolve = (key: string, contact: Parameters<typeof resolveContactPanelField>[1]) => resolveContactPanelField(key, contact, lookups)

describe('reserved panel keys', () => {
  it('are namespaced with a colon, which no session variable name can contain', () => {
    expect(CONTACT_PANEL_FIELDS.map(f => f.key)).toEqual(['contact:type', 'contact:cpf_cnpj', 'contact:unit', 'contact:department'])
    for (const f of CONTACT_PANEL_FIELDS) expect(isContactPanelField(f.key)).toBe(true)
    for (const key of ['order_id', 'contact', 'contact_type', 'a.b', 'unit']) expect(isContactPanelField(key)).toBe(false)
  })
})

describe('resolveContactPanelField', () => {
  it('type: always visible, with the translated label (cliente when unset)', () => {
    expect(resolve('contact:type', { contact_type: 'colaborador' })).toEqual({ visible: true, value: 'Colaborador' })
    expect(resolve('contact:type', { contact_type: 'fornecedor' })).toEqual({ visible: true, value: 'Fornecedor' })
    expect(resolve('contact:type', {})).toEqual({ visible: true, value: 'Cliente' })
  })

  it('document: visible only with a value, no empty label', () => {
    expect(resolve('contact:cpf_cnpj', { cpf_cnpj: '52998224725' })).toEqual({ visible: true, value: '52998224725' })
    for (const empty of [undefined, null, '']) expect(resolve('contact:cpf_cnpj', { cpf_cnpj: empty }).visible).toBe(false)
  })

  it('unit and department: only for a colaborador that has them', () => {
    const colab = { contact_type: 'colaborador', unit_id: 'u1', department_id: 'd1' }
    expect(resolve('contact:unit', colab)).toEqual({ visible: true, value: 'Feira 2' })
    expect(resolve('contact:department', colab)).toEqual({ visible: true, value: 'Expedição' })

    // linked but the name cannot be read (no access to the lists)
    expect(resolve('contact:unit', { ...colab, unit_id: 'unknown' })).toEqual({ visible: true, value: 'Definido' })
    expect(resolve('contact:department', { ...colab, department_id: 'unknown' })).toEqual({ visible: true, value: 'Definido' })

    // a colaborador without them
    expect(resolve('contact:unit', { contact_type: 'colaborador' }).visible).toBe(false)
    expect(resolve('contact:department', { contact_type: 'colaborador', department_id: null }).visible).toBe(false)

    // cliente and fornecedor: they do not apply, even with stale values
    for (const type of ['cliente', 'fornecedor', undefined]) {
      expect(resolve('contact:unit', { contact_type: type, unit_id: 'u1' }).visible).toBe(false)
      expect(resolve('contact:department', { contact_type: type, department_id: 'd1' }).visible).toBe(false)
    }
  })

  it('reflects the contact as it is now: the same key gives a new value after an edit', () => {
    expect(resolve('contact:unit', { contact_type: 'colaborador', unit_id: 'u1' }).value).toBe('Feira 2')
    expect(resolve('contact:unit', { contact_type: 'cliente', unit_id: 'u1' }).visible).toBe(false)
  })

  it('an unknown reserved key shows nothing', () => {
    expect(resolve('contact:something', { contact_type: 'colaborador' }).visible).toBe(false)
  })
})
