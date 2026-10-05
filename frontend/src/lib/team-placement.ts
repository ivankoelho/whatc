import type { Team } from '@/services/api'

// A flow transfer node routed "by the contact's unit and department" needs exactly one active
// team tagged with that pair: with two or more the routing is ambiguous and the node falls back.
// These are the OTHER active teams that share the pair of a team being edited (both fields must
// be set for a pair to exist).
export function teamsSharingPlacement(teams: Team[], unitId?: string | null, departmentId?: string | null, exceptTeamId?: string): Team[] {
  if (!unitId || !departmentId) return []
  return teams.filter(t =>
    t.id !== exceptTeamId && t.is_active && t.unit_id === unitId && t.department_id === departmentId,
  )
}
