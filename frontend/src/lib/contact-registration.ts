import type { ContactType } from '@/stores/contacts'

export const CONTACT_TYPES: readonly ContactType[] = ['cliente', 'fornecedor', 'colaborador']

// Only a colaborador has a unit and a department. For a cliente or a fornecedor they do not
// apply and are not shown. The server enforces the same rule, this only keeps the screens in step.
export function showsPlacement(type?: string | null): boolean {
  return type === 'colaborador'
}

// The unit/department of an update payload. '' means "clear" to the API: out of colaborador both
// are sent cleared, whatever the form still holds.
export function placementPayload(type: string, unitId: string, departmentId: string): { unit_id: string; department_id: string } {
  if (!showsPlacement(type)) return { unit_id: '', department_id: '' }
  return { unit_id: unitId, department_id: departmentId }
}

// --- Registration fields in a flow's contact-panel configuration ---
//
// A flow's panel config lists fields by `key`. Besides session variables, a field can be one of the
// contact's own registration data. Those keys are RESERVED: they start with "contact:", and a
// session variable name can never contain ":" (names are letters, digits, "_" and ".").

export const CONTACT_PANEL_FIELDS = [
  { key: 'contact:type', labelKey: 'contacts.type' },
  { key: 'contact:cpf_cnpj', labelKey: 'contacts.document' },
  { key: 'contact:unit', labelKey: 'contacts.unit' },
  { key: 'contact:department', labelKey: 'contacts.department' },
] as const

export function isContactPanelField(key: string): boolean {
  return key.startsWith('contact:')
}

export interface ContactPanelData {
  contact_type?: string | null
  cpf_cnpj?: string | null
  unit_id?: string | null
  department_id?: string | null
}

export interface ContactPanelLookups {
  typeLabel: (type: string) => string
  unitName: (id: string) => string | undefined
  departmentName: (id: string) => string | undefined
  defined: string // shown when the link exists but its name cannot be read
}

export interface ResolvedPanelField {
  visible: boolean
  value: string
}

// What a registration field of the panel shows for a contact, from the CURRENT contact (never a
// copy kept in the session). A field with nothing to show is not visible: no label either.
// Unit and department only apply to a colaborador (showsPlacement).
export function resolveContactPanelField(key: string, contact: ContactPanelData, lookups: ContactPanelLookups): ResolvedPanelField {
  const hidden: ResolvedPanelField = { visible: false, value: '' }
  switch (key) {
    case 'contact:type': {
      const type = contact.contact_type || 'cliente'
      return { visible: true, value: lookups.typeLabel(type) }
    }
    case 'contact:cpf_cnpj':
      return contact.cpf_cnpj ? { visible: true, value: contact.cpf_cnpj } : hidden
    case 'contact:unit':
      if (!showsPlacement(contact.contact_type) || !contact.unit_id) return hidden
      return { visible: true, value: lookups.unitName(contact.unit_id) || lookups.defined }
    case 'contact:department':
      if (!showsPlacement(contact.contact_type) || !contact.department_id) return hidden
      return { visible: true, value: lookups.departmentName(contact.department_id) || lookups.defined }
    default:
      return hidden
  }
}
