import type { Occurrence } from '@/services/api'

export function slaStatus(occ: Occurrence): 'overdue' | 'on_time' | 'none' {
  if (!occ.sla_resolution_deadline) return 'none'
  if (occ.sla_breached || (!occ.closed_at && new Date(occ.sla_resolution_deadline) < new Date())) return 'overdue'
  return 'on_time'
}
