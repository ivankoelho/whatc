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
